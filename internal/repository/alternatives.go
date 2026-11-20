package repository

import (
	"context"
	"encoding/json"
	"geoseulleong/internal/alternatives"
	"geoseulleong/internal/odsay"
	"sync"
	"time"
)

// OriginalBoarding reuses the official GBIS namespace and unique route-visit matching.
func (j *JourneyReferences) OriginalBoarding(ctx context.Context, l odsay.Lane, leg odsay.SubPath) (int64, int64, error) {
	if l.Namespace != "" && l.Namespace != "odsay" {
		return 0, 0, alternatives.ErrIdentity
	}
	if l.Provider == nil || *l.Provider != 2 || l.LocalID == "" || leg.StartProvider == nil || *leg.StartProvider != 2 || leg.StartLocal == "" {
		return 0, 0, alternatives.ErrIdentity
	}
	ctx, cancel := bound(ctx)
	defer cancel()
	rows, e := j.db.Query(ctx, `SELECT r.route_id FROM bus_routes r JOIN route_external_ids x USING(route_id) WHERE x.source_id='gbis' AND x.external_route_id=$1 AND r.route_number=$2 AND r.route_type_raw IN ('11','14')`, l.LocalID, l.BusNumber)
	if e != nil {
		return 0, 0, ErrQuery
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) != nil {
			rows.Close()
			return 0, 0, ErrQuery
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, 0, ErrQuery
	}
	if len(ids) != 1 {
		return 0, 0, alternatives.ErrIdentity
	}
	v, _, e := j.find(ctx, ids[0], leg.StartLocal, leg.StartName, leg.StartARS, leg.StartProvider)
	if e != nil {
		return 0, 0, e
	}
	if v == nil {
		return 0, 0, alternatives.ErrIdentity
	}
	return ids[0], v.ID, nil
}

type alternativeSnapshot struct {
	data alternatives.Snapshot
	at   time.Time
}
type AlternativesSource struct {
	mu     sync.Mutex
	cache  map[int64]alternativeSnapshot
	db     DB
	path   string
	once   sync.Once
	files  []alternatives.Observation
	code50 int
	err    error
}

func NewAlternativesSource(db DB, path string) *AlternativesSource {
	return &AlternativesSource{db: db, path: path, cache: map[int64]alternativeSnapshot{}}
}
func (a *AlternativesSource) Load(ctx context.Context, id int64) (alternatives.Snapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return alternatives.Snapshot{}, e
	}
	now := time.Now()
	if v, ok := a.cache[id]; ok && now.Sub(v.at) < 30*time.Second {
		return v.data, nil
	}
	result, e := a.load(ctx, id)
	if e == nil {
		if len(a.cache) >= 64 {
			for k, v := range a.cache {
				if now.Sub(v.at) >= 30*time.Second {
					delete(a.cache, k)
				}
			}
		}
		if len(a.cache) < 64 {
			a.cache[id] = alternativeSnapshot{result, time.Now()}
		}
	}
	return result, e
}
func (a *AlternativesSource) load(ctx context.Context, id int64) (alternatives.Snapshot, error) {
	a.once.Do(func() {
		if a.path != "" {
			a.files, a.code50, a.err = alternatives.LoadFiles(a.path)
		}
	})
	if a.err != nil {
		return alternatives.Snapshot{}, a.err
	}
	ctx, cancel := bound(ctx)
	defer cancel()
	s := alternatives.Snapshot{RouteID: id}
	// Require unique official GBIS and verified STCIS route identities.
	// The optional legacy file fallback remains M5121-only.
	var routes int
	if e := a.db.QueryRow(ctx, `SELECT count(*),coalesce(max(x.external_route_id),'') FROM bus_routes r JOIN route_external_ids x USING(route_id) WHERE r.route_id=$1 AND r.route_type_raw IN ('11','14') AND x.source_id='gbis'`, id).Scan(&routes, &s.GBIS); e != nil {
		return s, ErrQuery
	}
	if routes != 1 {
		return s, alternatives.ErrIdentity
	}
	var mappings int
	if e := a.db.QueryRow(ctx, `SELECT count(DISTINCT external_route_id),coalesce(max(external_route_id),''),coalesce(max(service_date)::text,'') FROM provider_stop_mappings WHERE provider='stcis' AND route_id=$1 AND status='verified'`, id).Scan(&mappings, &s.STCIS, &s.MappingDate); e != nil {
		return s, ErrQuery
	}
	if mappings != 1 || s.STCIS == "" || s.MappingDate == "" {
		return s, alternatives.ErrIdentity
	}
	var raw []byte
	e := a.db.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('visit_id',v.route_stop_id,'sequence',v.stop_sequence,'turn_sequence',v.turn_seq,'gbis_stop_id',sx.external_stop_id,'original_name',v.stop_name,'via_name',v.is_via_name,'boarding_allowed',v.boarding_allowed,'point',CASE WHEN v.location IS NULL THEN NULL ELSE jsonb_build_object('lon',ST_X(v.location),'lat',ST_Y(v.location)) END,'crs_status',v.stop_crs_status,'mapping_count',m.n,'stcis_stop_id',m.stop,'stcis_sequence',m.seq) ORDER BY v.stop_sequence),'[]'::jsonb)
 FROM qgis_route_stops_current v JOIN stop_external_ids sx ON sx.stop_id=v.stop_id AND sx.source_id='gbis'
 LEFT JOIN LATERAL(SELECT count(*) n,coalesce(max(external_stop_id),'') stop,max(source_sequence) seq FROM provider_stop_mappings WHERE provider='stcis' AND route_id=v.route_id AND route_stop_id=v.route_stop_id AND service_date=$2::date AND status='verified' AND external_route_id=$3)m ON true WHERE v.route_id=$1`, id, s.MappingDate, s.STCIS).Scan(&raw)
	if e != nil {
		return s, ErrQuery
	}
	if json.Unmarshal(raw, &s.Visits) != nil {
		return s, ErrInvalidData
	}
	e = a.db.QueryRow(ctx, `WITH chosen AS (
 SELECT DISTINCT ON(r.service_date,r.stcis_route_id,r.ctpv_cd,r.sgg_cd) r.*
 FROM congestion_analysis_regions r JOIN congestion_analysis_sets a USING(analysis_set_id)
 WHERE r.stcis_route_id=$1
 ORDER BY r.service_date,r.stcis_route_id,r.ctpv_cd,r.sgg_cd,a.created_at DESC,r.analysis_set_id DESC
 ) SELECT coalesce(jsonb_agg(jsonb_build_object('date',o.service_date::text,'stop',o.stcis_stop_id,'sequence',o.stop_sequence,'tzon',o.time_code,'cgst',o.cgst_raw) ORDER BY o.batch_id,o.page_no,o.row_no),'[]'::jsonb)
 FROM chosen c JOIN congestion_observations o USING(batch_id) WHERE c.status='complete'`, s.STCIS).Scan(&raw)
	if e != nil {
		return s, ErrQuery
	}
	if json.Unmarshal(raw, &s.Rows) != nil {
		return s, ErrInvalidData
	}
	s.BaselineCount = len(s.Rows)
	// DB snapshots win; file fallback cannot double-count an already stored day.
	dbDates := map[string]bool{}
	for _, row := range s.Rows {
		dbDates[row.Date] = true
	}
	for _, row := range a.files {
		if s.STCIS == "41002905" && !dbDates[row.Date] {
			s.Rows = append(s.Rows, row)
			s.FileCount++
		}
	}
	if s.STCIS == "41002905" {
		s.Code50 = a.code50
	}
	return s, nil
}
