package repository

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
)

var ErrAnalysisSet = errors.New("analysis set not found for route/date")
var ErrVisit = errors.New("route visit not found")

type Congestion struct{ db DB }

func NewCongestion(db DB) *Congestion { return &Congestion{db: db} }

var congestionPolicy = map[string]string{"cgst_policy": "cgst-percent-v1", "cgst_interpretation_status": "hypothesis_unverified", "tzon_mapping_status": "unverified", "historical_mapping_status": "unverified"}

func (c *Congestion) scope(ctx context.Context, routeID int64, date, set string) error {
	var exists bool
	if c.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM congestion_analysis_sets a JOIN congestion_analysis_regions r USING(analysis_set_id) WHERE a.analysis_set_id=$1 AND (a.manifest->>'internal_route_id'=$2::bigint::text OR r.stcis_route_id IN (SELECT stcis_route_id FROM congestion_batches WHERE internal_route_id=$2)) AND r.service_date=$3::text::date)`, set, routeID, date).Scan(&exists) != nil {
		return ErrQuery
	}
	if !exists {
		return ErrAnalysisSet
	}
	return nil
}
func (c *Congestion) RouteProfile(ctx context.Context, routeID int64, date, set string) (json.RawMessage, error) {
	ctx, cancel := bound(ctx)
	defer cancel()
	if e := c.scope(ctx, routeID, date, set); e != nil {
		return nil, e
	}
	var raw []byte
	e := c.db.QueryRow(ctx, `SELECT jsonb_build_object('route_id',$2::bigint,'service_date',$3::text,'analysis_set_id',$1::text,'status','available','observed_stop_count',(SELECT count(DISTINCT stcis_stop_id) FROM congestion_selected_observations WHERE analysis_set_id=$1 AND service_date=$3::text::date),'observation_count',(SELECT count(*) FROM congestion_selected_observations WHERE analysis_set_id=$1 AND service_date=$3::text::date),'data_provided_regions',count(*) FILTER(WHERE status='complete'),'no_data_regions',count(*) FILTER(WHERE status='no_data_for_query'),'error_regions',count(*) FILTER(WHERE status='error'),'regions',jsonb_agg(jsonb_build_object('province',ctpv_cd,'district',sgg_cd,'status',status) ORDER BY ctpv_cd,sgg_cd),'policy',$4::jsonb) FROM congestion_analysis_regions WHERE analysis_set_id=$1 AND service_date=$3::text::date`, set, routeID, date, policyJSON()).Scan(&raw)
	if e != nil {
		return nil, ErrQuery
	}
	return json.RawMessage(raw), nil
}
func policyJSON() string { b, _ := json.Marshal(congestionPolicy); return string(b) }
func (c *Congestion) VisitProfile(ctx context.Context, routeID, visitID int64, date, set, tzon string) (json.RawMessage, error) {
	ctx, cancel := bound(ctx)
	defer cancel()
	if e := c.scope(ctx, routeID, date, set); e != nil {
		return nil, e
	}
	var name string
	var seq int
	e := c.db.QueryRow(ctx, `SELECT s.stop_name,r.stop_sequence FROM route_stops r JOIN bus_stops s USING(stop_id) WHERE r.route_id=$1 AND r.route_stop_id=$2`, routeID, visitID).Scan(&name, &seq)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, ErrVisit
		}
		return nil, ErrQuery
	}
	var raw []byte
	// A candidate is found by its explicit visit candidate, never by name.
	e = c.db.QueryRow(ctx, `WITH mappings AS (
 SELECT m.* FROM provider_stop_mappings m WHERE m.provider='stcis' AND m.route_id=$2 AND m.service_date=$3::text::date AND (m.route_stop_id=$4 OR m.candidates @> jsonb_build_array(jsonb_build_object('route_stop_id',$4::bigint))) AND EXISTS(SELECT 1 FROM congestion_analysis_regions r WHERE r.analysis_set_id=$1 AND r.service_date=m.service_date AND r.stcis_route_id=m.external_route_id)
 ), matches AS (SELECT count(*) n FROM mappings), chosen AS (SELECT * FROM mappings WHERE (SELECT n FROM matches)=1), stats AS (
 SELECT count(*) AS n,count(o.cgst_raw) AS numeric_n,avg(o.cgst_raw) AS avg,min(o.cgst_raw) AS min,max(o.cgst_raw) AS max,percentile_cont(ARRAY[0.5,0.75,0.9,0.95]) WITHIN GROUP(ORDER BY o.cgst_raw::float8) AS q
 FROM congestion_selected_observations o JOIN chosen m ON m.external_route_id=o.stcis_route_id AND m.external_stop_id=o.stcis_stop_id AND m.source_sequence=o.stop_sequence
 WHERE o.analysis_set_id=$1 AND o.service_date=$3::text::date AND m.status='verified' AND ($5::text='' OR o.time_code=$5)
 ) SELECT jsonb_build_object('route_id',$2::bigint,'route_stop_id',$4::bigint,'stop_name',$6::text,'stop_sequence',$7::int,'service_date',$3::text,'analysis_set_id',$1::text,'tzon',nullif($5::text,''),'stcis_stop_id',(SELECT external_stop_id FROM chosen),'mapping_status',CASE WHEN (SELECT n FROM matches)>1 THEN 'ambiguous' ELSE coalesce((SELECT status FROM chosen),'unmapped') END,'status',CASE WHEN (SELECT n FROM matches)>1 THEN 'mapping_ambiguous' WHEN EXISTS(SELECT 1 FROM chosen WHERE status<>'verified') THEN 'mapping_unverified' WHEN n=0 THEN 'no_observations' ELSE 'available' END,'observation_count',n,'numeric_count',numeric_n,'mean_cgst',avg,'p50',q[1],'p75',q[2],'p90',q[3],'p95',q[4],'min_cgst',min,'max_cgst',max,'policy',$8::jsonb) FROM stats`, set, routeID, date, visitID, tzon, name, seq, policyJSON()).Scan(&raw)
	if e != nil {
		return nil, ErrQuery
	}
	return json.RawMessage(raw), nil
}
