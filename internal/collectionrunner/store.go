package collectionrunner

import (
	"context"
	"encoding/json"
	"errors"
	"geoseulleong/internal/collectionpreview"
	cp "geoseulleong/internal/congestionpipeline"
	rc "geoseulleong/internal/routecongestion"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Store struct{ Pool *pgxpool.Pool }

// ApplyIsolated retains the existing isolated test admission.
func (s Store) ApplyIsolated(ctx context.Context, p *Permit, run RunResult) (string, error) {
	if s.Pool == nil || p == nil {
		return "", errors.New("store/permit required")
	}
	if e := CheckIsolated(ctx, s.Pool); e != nil {
		return "", e
	}
	return s.apply(ctx, p, run, false)
}

// ApplyDatabase is the explicit importer path. The CLI backs up before calling it.
// HTTP repositories remain read-only; no environment marker enables writes.
func (s Store) ApplyDatabase(ctx context.Context, p *Permit, run RunResult, database string) (string, error) {
	if s.Pool == nil || p == nil || database != "geoseulleong" {
		return "", errors.New("explicit geoseulleong import target required")
	}
	c := s.Pool.Config().ConnConfig
	if c.Database != database {
		return "", errors.New("configured database mismatch")
	}
	var actual string
	if s.Pool.QueryRow(ctx, "SELECT current_database()").Scan(&actual) != nil || actual != database {
		return "", errors.New("actual database mismatch")
	}
	return s.apply(ctx, p, run, false)
}

// ApplyAvailable imports only validated successful scopes and labels an incomplete set.
func (s Store) ApplyAvailable(ctx context.Context, p *Permit, run RunResult, database string) (string, error) {
	if s.Pool == nil || p == nil || database != "geoseulleong" || s.Pool.Config().ConnConfig.Database != database {
		return "", errors.New("explicit import target required")
	}
	var actual string
	if s.Pool.QueryRow(ctx, "SELECT current_database()").Scan(&actual) != nil || actual != database {
		return "", errors.New("actual database mismatch")
	}
	return s.apply(ctx, p, run, true)
}
func (s Store) apply(ctx context.Context, p *Permit, run RunResult, partial bool) (string, error) {
	fresh, e := Prepare(p.Config)
	if e != nil || fresh.Hash != p.Hash || run.Authorization != p.Hash {
		return "", errors.New("authorization conflict")
	}
	p = fresh
	if (!partial && len(run.Results) != len(p.scopes)) || (partial && (!run.Incomplete || len(run.Results) == 0 || len(run.Results)+len(run.Failed) != len(p.scopes))) {
		return "", errors.New("incomplete run cannot select analysis set")
	}
	seen := map[rc.Scope]bool{}
	results := append([]Result(nil), run.Results...)
	sort.Slice(results, func(i, j int) bool {
		return collectionpreview.ScopeKey(results[i].Scope) < collectionpreview.ScopeKey(results[j].Scope)
	})
	selections := []map[string]any{}
	for _, r := range results {
		if !p.Allows(r.Scope) || seen[r.Scope] {
			return "", errors.New("duplicate/unapproved scope")
		}
		seen[r.Scope] = true
		if r.Status == "complete" {
			if r.Batch == nil || r.Outcome != nil {
				return "", errors.New("batch missing")
			}
			b, e := cp.BuildAuthorized(r.Scope, r.Batch.Pages, p.Allows)
			if e != nil || b.ID != r.ID || b.Total != r.Batch.Total || r.Batch.Scope != r.Scope {
				return "", errors.New("batch validation failed")
			}
		}
		if r.Status == "no_data_for_query" {
			if r.Outcome == nil || r.Batch != nil {
				return "", errors.New("query missing")
			}
			q := r.Outcome
			v, e := rc.Parse(q.Raw)
			b, _ := json.Marshal(r.Scope)
			if !errors.Is(e, rc.ErrNoData) || v.Code != "50" || q.HTTP != 200 || q.SHA != Digest(q.Raw) || q.Scope != r.Scope || q.Plan != p.Hash || q.Page != 1 || q.At.IsZero() || r.ID != Digest(append(b, []byte(q.SHA)...)) {
				return "", errors.New("code50 validation failed")
			}
		}
		if r.Status != "complete" && r.Status != "no_data_for_query" {
			return "", errors.New("nonterminal result")
		}
		selections = append(selections, map[string]any{"scope": r.Scope, "snapshot_id": r.ID, "status": r.Status})
	}
	for _, scope := range run.Failed {
		if !p.Allows(scope) || seen[scope] {
			return "", errors.New("invalid incomplete set")
		}
		seen[scope] = true
	}
	proofs := map[string]string{}
	for _, route := range p.Config.Manifest.Routes {
		proofs[route.STCIS] = route.EvidenceSHA
	}
	manifestData := map[string]any{"version": "generic-collection-selection-v1", "selections": selections, "policy": json.RawMessage(cp.Policy), "route_identity_evidence_sha256": proofs}
	if partial {
		manifestData["incomplete"] = true
		manifestData["missing_scopes"] = run.Failed
	}
	manifest, _ := json.Marshal(manifestData)
	setID := Digest(manifest)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if e != nil {
		return "", errors.New("transaction unavailable")
	}
	defer tx.Rollback(context.Background())
	// One atomic bounded run; no partial analysis set can become visible.
	for _, r := range results {
		scope := r.Scope
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, collectionpreview.ScopeKey(scope)); e != nil {
			return "", e
		}
		id := p.identities[scope.Route]
		var n int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM bus_routes b JOIN route_external_ids x USING(route_id) WHERE b.route_id=$1 AND b.route_number=$2 AND x.source_id='gbis' AND x.external_route_id=$3 AND x.verified_at IS NOT NULL`, id.Internal, id.Name, id.GBIS).Scan(&n); e != nil || n != 1 {
			return "", errors.New("DB route identity CAS failed")
		}
		// Refuse another immutable snapshot of the same scope, including code50 vs zero.
		var conflicts int
		e = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT batch_id AS id FROM congestion_batches WHERE service_date=$1::text::date AND stcis_route_id=$2 AND ctpv_cd=$3 AND sgg_cd=$4 UNION ALL SELECT query_id FROM congestion_query_outcomes WHERE service_date=$1::text::date AND stcis_route_id=$2 AND ctpv_cd=$3 AND sgg_cd=$4) x WHERE id<>$5`, scope.Date, scope.Route, scope.Province, scope.District, r.ID).Scan(&conflicts)
		if e != nil || conflicts != 0 {
			return "", errors.New("same scope already has different snapshot; explicit selection review required")
		}
		if r.Batch != nil {
			if e = saveBatch(ctx, tx, id.Internal, *r.Batch); e != nil {
				return "", e
			}
		} else {
			q := r.Outcome
			if _, e = tx.Exec(ctx, `INSERT INTO congestion_query_outcomes(query_id,service_date,stcis_route_id,ctpv_cd,sgg_cd,http_status,api_code,status,response_sha256,raw_response,collected_at) VALUES($1,$2::text::date,$3,$4,$5,200,'50','no_data_for_query',$6,$7::jsonb,$8) ON CONFLICT DO NOTHING`, r.ID, scope.Date, scope.Route, scope.Province, scope.District, q.SHA, string(q.Raw), q.At); e != nil {
				return "", errors.New("query insert failed")
			}
			var matches bool
			if e = tx.QueryRow(ctx, `SELECT response_sha256=$2 AND raw_response=$3::jsonb AND status='no_data_for_query' AND api_code='50' FROM congestion_query_outcomes WHERE query_id=$1`, r.ID, q.SHA, string(q.Raw)).Scan(&matches); e != nil || !matches {
				return "", errors.New("existing query conflict")
			}
		}
	}
	var existing json.RawMessage
	e = tx.QueryRow(ctx, `SELECT manifest FROM congestion_analysis_sets WHERE analysis_set_id=$1`, setID).Scan(&existing)
	if e == nil {
		var equal bool
		var n int
		if tx.QueryRow(ctx, `SELECT manifest=$2::jsonb,(SELECT count(*) FROM congestion_analysis_regions WHERE analysis_set_id=$1) FROM congestion_analysis_sets WHERE analysis_set_id=$1`, setID, string(manifest)).Scan(&equal, &n) != nil || !equal || n != len(results) {
			return "", errors.New("existing analysis set conflict")
		}
		for _, r := range results {
			var ok bool
			var targetColumn string
			if r.Batch != nil {
				targetColumn = "batch_id"
			} else {
				targetColumn = "query_id"
			}
			// Column is chosen from two constants, never supplied by the caller.
			query := `SELECT ` + targetColumn + `=$6 AND status=$7 FROM congestion_analysis_regions WHERE analysis_set_id=$1 AND service_date=$2::text::date AND stcis_route_id=$3 AND ctpv_cd=$4 AND sgg_cd=$5`
			if tx.QueryRow(ctx, query, setID, r.Scope.Date, r.Scope.Route, r.Scope.Province, r.Scope.District, r.ID, r.Status).Scan(&ok) != nil || !ok {
				return "", errors.New("existing selection link conflict")
			}
		}
		return "duplicate", nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return "", e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO congestion_analysis_sets(analysis_set_id,manifest) VALUES($1,$2::jsonb)`, setID, string(manifest)); e != nil {
		return "", e
	}
	for _, r := range results {
		var b, q any
		if r.Batch != nil {
			b = r.ID
		} else {
			q = r.ID
		}
		if _, e = tx.Exec(ctx, `INSERT INTO congestion_analysis_regions(analysis_set_id,service_date,stcis_route_id,ctpv_cd,sgg_cd,batch_id,query_id,status) VALUES($1,$2::text::date,$3,$4,$5,$6,$7,$8)`, setID, r.Scope.Date, r.Scope.Route, r.Scope.Province, r.Scope.District, b, q, r.Status); e != nil {
			return "", e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return "", errors.New("commit uncertain; inspect stored IDs before retry")
	}
	return "inserted", nil
}
func saveBatch(ctx context.Context, tx pgx.Tx, internal int64, b cp.Batch) error {
	var inserted string
	e := tx.QueryRow(ctx, `INSERT INTO congestion_batches(batch_id,source,service_date,stcis_route_id,ctpv_cd,sgg_cd,internal_route_id,total_count,page_count,interpretation_version,interpretation_policy) VALUES($1,'molit_route_congestion',$2::text::date,$3,$4,$5,$6,$7,$8,$9,$10::jsonb) ON CONFLICT DO NOTHING RETURNING batch_id`, b.ID, b.Scope.Date, b.Scope.Route, b.Scope.Province, b.Scope.District, internal, b.Total, len(b.Pages), cp.PolicyVersion, string(cp.Policy)).Scan(&inserted)
	if errors.Is(e, pgx.ErrNoRows) {
		var n int
		var total int
		if tx.QueryRow(ctx, `SELECT total_count,(SELECT count(*) FROM congestion_observations WHERE batch_id=$1) FROM congestion_batches WHERE batch_id=$1`, b.ID).Scan(&total, &n) != nil || n != b.Total || total != b.Total {
			return errors.New("existing batch count conflict")
		}
		for _, p := range b.Pages {
			var ok bool
			if tx.QueryRow(ctx, `SELECT response_sha256=$3 AND raw_response=$4::jsonb FROM congestion_pages WHERE batch_id=$1 AND page_no=$2`, b.ID, p.Number, p.SHA, string(p.Raw)).Scan(&ok) != nil || !ok {
				return errors.New("existing page conflict")
			}
			for j, r := range p.Rows {
				if tx.QueryRow(ctx, `SELECT raw_record=$4::jsonb FROM congestion_observations WHERE batch_id=$1 AND page_no=$2 AND row_no=$3`, b.ID, p.Number, j+1, string(r)).Scan(&ok) != nil || !ok {
					return errors.New("existing observation conflict")
				}
			}
		}
		return nil
	}
	if e != nil {
		return errors.New("batch insert failed")
	}
	for _, p := range b.Pages {
		if p.At.IsZero() {
			return errors.New("missing response timestamp")
		}
		if _, e = tx.Exec(ctx, `INSERT INTO congestion_pages(batch_id,page_no,http_status,record_count,response_sha256,collected_at,raw_response) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb)`, b.ID, p.Number, p.HTTP, len(p.Rows), p.SHA, p.At, string(p.Raw)); e != nil {
			return e
		}
		for j, r := range p.Rows {
			var seq any
			if v := cp.Value(r, "sttn_seq"); v != "" && v != "null" {
				n, e := strconv.Atoi(v)
				if e != nil {
					return errors.New("invalid stop sequence")
				}
				seq = n
			}
			var cgst any
			if v := cp.Value(r, "cgst"); v != "" && v != "null" {
				cgst = v
			}
			_, e = tx.Exec(ctx, `INSERT INTO congestion_observations(batch_id,page_no,row_no,service_date,stcis_route_id,ctpv_cd,sgg_cd,stcis_stop_id,stop_sequence,trip_ordinal,time_code,cgst_raw,raw_record) VALUES($1,$2,$3,$4::text::date,$5,$6,$7,$8,$9,$10,$11,$12::text::numeric,$13::jsonb)`, b.ID, p.Number, j+1, b.Scope.Date, b.Scope.Route, b.Scope.Province, b.Scope.District, cp.Value(r, "sttn_id"), seq, cp.Value(r, "opr_trntm"), cp.Value(r, "tzon"), cgst, string(r))
			if e != nil {
				return errors.New("observation insert failed; transaction rolled back")
			}
		}
	}
	return nil
}

// CheckIsolated checks both configured and actual server identity, including fallbacks.
func CheckIsolated(ctx context.Context, p *pgxpool.Pool) error {
	if p == nil {
		return errors.New("isolated pool required")
	}
	c := p.Config().ConnConfig
	if !strings.HasPrefix(c.Host, "/tmp/geoseulleong-runner.") || c.Port != 55459 || c.Database != "postgres" {
		return errors.New("writes restricted to isolated runner cluster")
	}
	for _, f := range c.Fallbacks {
		if f.Host != c.Host || f.Port != c.Port {
			return errors.New("nonisolated database fallback refused")
		}
	}
	var database, dir, port string
	if p.QueryRow(ctx, `SELECT current_database(),current_setting('data_directory'),current_setting('port')`).Scan(&database, &dir, &port) != nil {
		return errors.New("isolated server identity unavailable")
	}
	host, e := filepath.EvalSymlinks(c.Host)
	if e != nil {
		return errors.New("isolated socket unavailable")
	}
	physical, e := filepath.EvalSymlinks(dir)
	if e != nil || database != "postgres" || port != "55459" || physical != filepath.Join(host, "data") {
		return errors.New("actual server is not the isolated runner cluster")
	}
	return nil
}
