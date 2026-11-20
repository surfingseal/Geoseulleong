package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"geoseulleong/internal/congestionplan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type AnalysisStore struct{ Pool *pgxpool.Pool }

// SaveSelection is atomic; source batch transactions must have succeeded first.
// Reapplying exactly the same manifest never duplicates rows or silently upgrades mappings.
func (s AnalysisStore) SaveSelection(ctx context.Context, p congestionplan.Plan) (string, error) {
	return s.saveSelection(ctx, p, false)
}
func (s AnalysisStore) saveSelection(ctx context.Context, p congestionplan.Plan, reviewed bool) (string, error) {
	if e := p.Validate(); e != nil {
		return "", e
	}
	var compact bytes.Buffer
	if json.Compact(&compact, p.Manifest) != nil {
		return "", errors.New("manifest invalid")
	}
	h := fmt.Sprintf("%x", sha256.Sum256(compact.Bytes()))
	if h != p.ID || !json.Valid(p.Manifest) {
		return "", errors.New("analysis manifest hash mismatch")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return "", errors.New("selection transaction unavailable")
	}
	defer tx.Rollback(context.Background())
	if reviewed {
		for _, m := range p.Mappings {
			v := m.Result.Visit
			var id int64
			e = tx.QueryRow(ctx, `SELECT r.route_stop_id FROM route_stops r JOIN bus_stops s USING(stop_id) JOIN stop_external_ids x ON x.stop_id=r.stop_id AND x.source_id='gbis' WHERE r.route_stop_id=$1 AND r.route_id=$2 AND r.stop_id=$3 AND r.stop_sequence=$4 AND s.stop_name=$5 AND x.external_stop_id=$6 FOR SHARE OF r,s`, v.ID, p.RouteID, v.StopID, v.Sequence, v.Name, v.ExternalID).Scan(&id)
			if e != nil {
				return "", errors.New("reviewed GBIS visit CAS conflict")
			}
		}
	}

	var id string
	e = tx.QueryRow(ctx, `INSERT INTO congestion_analysis_sets(analysis_set_id,manifest) VALUES($1,$2::jsonb) ON CONFLICT DO NOTHING RETURNING analysis_set_id`, p.ID, string(p.Manifest)).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		var n int
		if tx.QueryRow(ctx, `SELECT count(*) FROM congestion_analysis_regions WHERE analysis_set_id=$1`, p.ID).Scan(&n) != nil || n != len(p.Batches)+len(p.Queries) {
			return "", errors.New("existing selection conflict")
		}
		return "duplicate", nil
	}
	if e != nil {
		return "", errors.New("selection insert failed")
	}
	for _, q := range p.Queries {
		h := fmt.Sprintf("%x", sha256.Sum256(q.Raw))
		if h != q.SHA {
			return "", errors.New("query raw hash mismatch")
		}
		if _, e = tx.Exec(ctx, `INSERT INTO congestion_query_outcomes(query_id,service_date,stcis_route_id,ctpv_cd,sgg_cd,http_status,api_code,status,response_sha256,raw_response,collected_at) VALUES($1,$2::text::date,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11) ON CONFLICT(query_id) DO NOTHING`, q.ID, q.Scope.Date, q.Scope.Route, q.Scope.Province, q.Scope.District, q.HTTP, q.Code, q.Status, q.SHA, string(q.Raw), q.At); e != nil {
			return "", errors.New("query outcome insert failed")
		}
	}
	for _, b := range p.Batches {
		if _, e = tx.Exec(ctx, `INSERT INTO congestion_analysis_regions(analysis_set_id,service_date,stcis_route_id,ctpv_cd,sgg_cd,batch_id,status) VALUES($1,$2::text::date,$3,$4,$5,$6,'complete')`, p.ID, b.Scope.Date, b.Scope.Route, b.Scope.Province, b.Scope.District, b.ID); e != nil {
			return "", errors.New("batch selection scope conflict")
		}
	}
	for _, q := range p.Queries {
		if _, e = tx.Exec(ctx, `INSERT INTO congestion_analysis_regions(analysis_set_id,service_date,stcis_route_id,ctpv_cd,sgg_cd,query_id,status) VALUES($1,$2::text::date,$3,$4,$5,$6,$7)`, p.ID, q.Scope.Date, q.Scope.Route, q.Scope.Province, q.Scope.District, q.ID, q.Status); e != nil {
			return "", errors.New("query selection scope conflict")
		}
	}
	for _, m := range p.Mappings {
		if m.Result.Status == "verified" && !reviewed {
			return "", errors.New("verified mapping requires separately reviewed metadata apply path")
		}
		var visitID, stopID, verifiedAt any
		if m.Result.Status == "verified" {
			visitID = m.Result.Visit.ID
			stopID = m.Result.Visit.StopID
			verifiedAt = time.Now().UTC()
		}
		evidence, _ := json.Marshal(map[string]any{"mapping": m, "provenance": p.MappingProvenance})
		candidates, _ := json.Marshal(m.Result.Candidates)
		// Existing evidence is never overwritten automatically; any difference aborts the selection.
		var matched int
		e = tx.QueryRow(ctx, `INSERT INTO provider_stop_mappings(provider,external_route_id,external_stop_id,source_sequence,service_date,route_id,status,method,evidence,candidates,route_stop_id,stop_id,verified_at) VALUES('stcis',$1,$2,$3,$4::text::date,$5,$6,$7,$8::jsonb,$9::jsonb,$10,$11,$12) ON CONFLICT(provider,external_route_id,external_stop_id,source_sequence,service_date) DO UPDATE SET evidence=provider_stop_mappings.evidence WHERE provider_stop_mappings.evidence=EXCLUDED.evidence AND provider_stop_mappings.status=EXCLUDED.status RETURNING 1`, m.Source.Route, m.Source.ID, m.Source.Sequence, p.Date, p.RouteID, m.Result.Status, m.Result.Method, string(evidence), string(candidates), visitID, stopID, verifiedAt).Scan(&matched)
		if e != nil {
			return "", errors.New("mapping insert or evidence conflict")
		}
	}
	if tx.Commit(ctx) != nil {
		return "", errors.New("selection commit failed; inspect before retry")
	}
	return "inserted", nil
}
