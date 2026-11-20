package repository

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// StopTimeProfiles always selects one explicit analysis snapshot, never all stored histories.
func (s AnalysisStore) StopTimeProfiles(ctx context.Context, setID string) (json.RawMessage, error) {
	b, e := hex.DecodeString(setID)
	if e != nil || len(b) != 32 {
		return nil, errors.New("invalid analysis set ID")
	}
	ctx, cancel := bound(ctx)
	defer cancel()
	var raw []byte
	if s.Pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY stcis_stop_id,stop_sequence,time_code),'[]'::jsonb) FROM congestion_stop_time_profiles t WHERE analysis_set_id=$1`, setID).Scan(&raw) != nil {
		return nil, ErrQuery
	}
	return json.RawMessage(raw), nil
}

// CandidateDistances preserves absent/unverified source coordinates as NULL.
// A metric distance is calculated only for an explicitly confirmed input CRS.
func (r *Routes) CandidateDistances(ctx context.Context, routeID int64, lon, lat *float64, source4326Confirmed bool) (json.RawMessage, error) {
	ctx, cancel := bound(ctx)
	defer cancel()
	var raw []byte
	e := r.db.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('route_stop_id',v.route_stop_id,'stop_id',v.stop_id,'stop_sequence',v.stop_sequence,'crs_status',v.stop_crs_status,'distance_m',CASE WHEN $4::boolean AND $2::float8 BETWEEN -180 AND 180 AND $3::float8 BETWEEN -90 AND 90 AND v.location IS NOT NULL THEN ST_Distance(ST_Transform(v.location,5179),ST_Transform(ST_SetSRID(ST_MakePoint($2::float8,$3::float8),4326),5179)) ELSE NULL END) ORDER BY v.stop_sequence),'[]'::jsonb) FROM qgis_route_stops_current v WHERE v.route_id=$1`, routeID, lon, lat, source4326Confirmed).Scan(&raw)
	if e != nil {
		return nil, ErrQuery
	}
	return json.RawMessage(raw), nil
}
