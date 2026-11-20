package repository

import (
	"context"
	"errors"
	"geoseulleong/internal/boarding"
	"math"
	"regexp"
	"time"
)

// Explicit dataset/date selection prevents accidental production multi-day claims.
type BoardingConfig struct {
	SetIDs        []string `json:"analysis_set_ids"`
	Dates         []string `json:"service_dates"`
	ReferenceDate string   `json:"mapping_reference_date"`
	// Day labels must be supplied and checked for every selected day. Never pool holidays.
	Calendar map[string]string `json:"calendar"`
}
type Boarding struct {
	db     DB
	config BoardingConfig
}

func NewBoarding(db DB, c BoardingConfig) (*Boarding, error) {
	if len(c.SetIDs) == 0 || len(c.SetIDs) > 16 || len(c.Dates) == 0 || len(c.Dates) > 366 {
		return nil, errors.New("invalid boarding dataset")
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, id := range c.SetIDs {
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(id) {
			return nil, ErrInvalidData
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	c.SetIDs = ids
	c.Dates = append([]string(nil), c.Dates...)
	calendar := map[string]string{}
	for _, d := range c.Dates {
		if _, e := time.Parse("2006-01-02", d); e != nil {
			return nil, ErrInvalidData
		}
		g := c.Calendar[d]
		if g != "weekday" && g != "holiday" && g != "weekend" {
			return nil, ErrInvalidData
		}
		calendar[d] = g
	}
	if _, e := time.Parse("2006-01-02", c.ReferenceDate); e != nil {
		return nil, ErrInvalidData
	}
	c.Calendar = calendar
	return &Boarding{db: db, config: c}, nil
}
func (b *Boarding) ResolveBoarding(ctx context.Context, r boarding.Request) (boarding.Stop, error) {
	ctx, cancel := bound(ctx)
	defer cancel()
	rows, e := b.db.Query(ctx, `SELECT v.stop_sequence,m.external_route_id,m.external_stop_id,m.source_sequence,
 CASE WHEN v.boarding_allowed IS TRUE THEN 'allowed' WHEN v.boarding_allowed IS FALSE THEN 'not_allowed' ELSE 'unverified' END,
 CASE WHEN v.turn_seq IS NULL OR v.turn_seq<=0 THEN 'unknown' WHEN v.stop_sequence=v.turn_seq THEN 'turning_point' WHEN v.stop_sequence<v.turn_seq THEN 'outbound_segment' ELSE 'return_segment' END
 FROM route_stops v JOIN stop_external_ids sx ON sx.stop_id=v.stop_id AND sx.source_id='gbis'
 JOIN route_external_ids rx ON rx.route_id=v.route_id AND rx.source_id='gbis'
 JOIN provider_stop_mappings m ON m.route_id=v.route_id AND m.route_stop_id=v.route_stop_id AND m.provider='stcis' AND m.status='verified' AND m.service_date=$5::text::date AND EXISTS(SELECT 1 FROM congestion_batches cb WHERE cb.internal_route_id=v.route_id AND cb.stcis_route_id=m.external_route_id)
 WHERE v.route_id=$1 AND v.route_stop_id=$2 AND rx.external_route_id=$3 AND sx.external_stop_id=$4`, r.RouteID, r.VisitID, r.GBISRoute, r.GBISStop, b.config.ReferenceDate)
	if e != nil {
		return boarding.Stop{}, ErrQuery
	}
	defer rows.Close()
	s := boarding.Stop{RouteID: r.RouteID, VisitID: r.VisitID, GBISRoute: r.GBISRoute, GBISStop: r.GBISStop, MappingDate: b.config.ReferenceDate}
	n := 0
	for rows.Next() {
		if rows.Scan(&s.Sequence, &s.STCISRoute, &s.STCISStop, &s.SourceSequence, &s.PermissionStatus, &s.VisitDirection) != nil {
			return s, ErrQuery
		}
		n++
	}
	if rows.Err() != nil {
		return s, ErrQuery
	}
	if n != 1 {
		return s, ErrVisit
	}
	return s, nil
}

// ValidateDataset rejects unknown sets, absent days, and competing snapshots for a scope.
func (b *Boarding) ValidateDataset(ctx context.Context, route int64) error {
	var sets, dates int
	var ambiguous bool
	err := b.db.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM congestion_analysis_sets WHERE analysis_set_id=ANY($1::text[])),
 (SELECT count(DISTINCT r.service_date) FROM congestion_analysis_regions r WHERE r.analysis_set_id=ANY($1::text[]) AND r.service_date::text=ANY($2::text[]) AND r.stcis_route_id IN (SELECT DISTINCT stcis_route_id FROM congestion_batches WHERE internal_route_id=$3)),
 EXISTS(SELECT 1 FROM congestion_analysis_regions r WHERE r.analysis_set_id=ANY($1::text[]) AND r.service_date::text=ANY($2::text[]) AND r.status='complete' GROUP BY r.service_date,r.stcis_route_id,r.ctpv_cd,r.sgg_cd HAVING count(DISTINCT r.batch_id)>1) OR EXISTS(SELECT 1 FROM congestion_analysis_regions r WHERE r.analysis_set_id=ANY($1::text[]) AND r.service_date::text=ANY($2::text[]) AND r.status='error')`, b.config.SetIDs, b.config.Dates, route).Scan(&sets, &dates, &ambiguous)
	if err != nil {
		return ErrQuery
	}
	if sets != len(b.config.SetIDs) || dates != len(b.config.Dates) || ambiguous {
		return ErrAnalysisSet
	}
	return nil
}

// DISTINCT batch IDs deduplicate selection references, NOT original repeated rows.
const BoardingRowsSQL = `WITH selected AS (
 SELECT DISTINCT r.batch_id FROM congestion_analysis_regions r JOIN congestion_batches b USING(batch_id)
 WHERE r.analysis_set_id=ANY($1::text[]) AND r.status='complete' AND r.service_date::text=ANY($2::text[]) AND b.internal_route_id=$3
 ) SELECT o.service_date::text,CASE WHEN o.cgst_raw::text IN ('NaN','Infinity','-Infinity') THEN NULL ELSE o.cgst_raw::float8 END AS cgst_raw,o.raw_record::text,o.batch_id
 FROM congestion_observations o JOIN selected s USING(batch_id)
 WHERE o.stcis_route_id=$4 AND o.stcis_stop_id=$5 AND o.stop_sequence=$6 AND o.time_code ~ '^[0-9]{1,2}$' AND o.time_code::int=$7::int`

func (b *Boarding) BoardingHistory(ctx context.Context, s boarding.Stop, t boarding.TimeContext) (boarding.History, error) {
	ctx, cancel := bound(ctx)
	defer cancel()
	out := boarding.History{Status: "unavailable", Groups: []boarding.Profile{}, SetIDs: append([]string(nil), b.config.SetIDs...), Unit: "percent", Interpretation: "project_user_rule_not_claim_of_official_formula", HistoricalMapping: "reference_pair_matched_historical_validity_unverified"}
	if e := b.ValidateDataset(ctx, s.RouteID); e != nil {
		return out, e
	}
	resolved, e := b.ResolveBoarding(ctx, boarding.Request{RouteID: s.RouteID, VisitID: s.VisitID, GBISRoute: s.GBISRoute, GBISStop: s.GBISStop})
	if e != nil || resolved != s {
		out.Status = "mapping_unverified"
		return out, nil
	}
	if t.Status != "matched_project_rule" {
		out.Status = "time_unknown"
		return out, nil
	}
	// SQL aggregates numeric values while preserving nonnumeric/duplicate observation counts.
	sql := `WITH observations AS (` + BoardingRowsSQL + `), classified AS (
 SELECT *,CASE WHEN service_date=ANY($8::text[]) THEN 'holiday' WHEN service_date=ANY($9::text[]) THEN 'weekend' ELSE 'weekday' END AS day_group FROM observations
 ) SELECT day_group,avg(cgst_raw),percentile_cont(0.9) WITHIN GROUP(ORDER BY cgst_raw),count(*),count(cgst_raw),count(DISTINCT raw_record),count(DISTINCT service_date),min(service_date),max(service_date),array_agg(DISTINCT service_date ORDER BY service_date),array_agg(DISTINCT batch_id ORDER BY batch_id) FROM classified GROUP BY day_group ORDER BY day_group`
	holiday, weekend := []string{}, []string{}
	for d, g := range b.config.Calendar {
		if g == "holiday" {
			holiday = append(holiday, d)
		}
		if g == "weekend" {
			weekend = append(weekend, d)
		}
	}
	rows, e := b.db.Query(ctx, sql, b.config.SetIDs, b.config.Dates, s.RouteID, s.STCISRoute, s.STCISStop, s.SourceSequence, t.TZON, holiday, weekend)
	if e != nil {
		return out, ErrQuery
	}
	defer rows.Close()
	for rows.Next() {
		var g boarding.Profile
		if rows.Scan(&g.DayGroup, &g.Mean, &g.P90, &g.Observations, &g.NumericCount, &g.UniqueJSON, &g.ObservedDays, &g.FirstDate, &g.LastDate, &g.Dates, &g.BatchIDs) != nil {
			return out, ErrQuery
		}
		if g.Mean != nil && (math.IsNaN(*g.Mean) || math.IsInf(*g.Mean, 0)) {
			return out, ErrInvalidData
		}
		g.LowSample = g.NumericCount < 30
		out.Groups = append(out.Groups, g)
		if g.NumericCount > 0 {
			out.Status = "available"
		}
	}
	if rows.Err() != nil {
		return out, ErrQuery
	}
	return out, nil
}
