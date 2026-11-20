package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"geoseulleong/internal/congestionpipeline"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strconv"
	"time"
)

type CongestionStore struct{ Pool *pgxpool.Pool }
type SaveResult struct {
	BatchID string `json:"batch_id"`
	Status  string `json:"status"`
	Rows    int    `json:"rows"`
}

// OpenIngest is used ONLY by an explicit --apply path; the HTTP pool stays read-only.
func OpenIngest(ctx context.Context) (*pgxpool.Pool, error) {
	c, e := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if e != nil {
		return nil, errors.New("invalid ingest database configuration")
	}
	if os.Getenv("DATABASE_URL") == "" && os.Getenv("PGDATABASE") == "" && os.Getenv("PGSERVICE") == "" {
		c.ConnConfig.Database = "geoseulleong"
	}
	c.MaxConns = 2
	c.MinConns = 0
	c.ConnConfig.ConnectTimeout = 5 * time.Second
	c.ConnConfig.RuntimeParams["application_name"] = "geoseulleong-explicit-ingest"
	c.ConnConfig.RuntimeParams["statement_timeout"] = "30000"
	c.ConnConfig.RuntimeParams["default_transaction_read_only"] = "off"
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, e := pgxpool.NewWithConfig(bounded, c)
	if e != nil {
		return nil, errors.New("ingest database connection failed")
	}
	if p.Ping(bounded) != nil {
		p.Close()
		return nil, errors.New("ingest database connection failed")
	}
	return p, nil
}
func (s CongestionStore) Save(ctx context.Context, b congestionpipeline.Batch) (SaveResult, error) {
	result := SaveResult{BatchID: b.ID, Rows: b.Total}
	checked, e := congestionpipeline.Build(b.Scope, b.Pages)
	if e != nil || !b.Complete || checked.ID != b.ID || checked.Total != b.Total {
		return result, errors.New("batch not validated")
	}
	var compactPolicy bytes.Buffer
	if json.Compact(&compactPolicy, b.Policy) != nil || compactPolicy.String() != string(congestionpipeline.Policy) {
		return result, errors.New("interpretation policy mismatch")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if e != nil {
		return result, errors.New("ingest transaction unavailable")
	}
	defer tx.Rollback(context.Background())
	var inserted string
	e = tx.QueryRow(ctx, `INSERT INTO public.congestion_batches(batch_id,source,service_date,stcis_route_id,ctpv_cd,sgg_cd,total_count,page_count,interpretation_version,interpretation_policy) VALUES($1,'molit_route_congestion',$2::text::date,$3,$4,$5,$6,$7,$8,$9::jsonb) ON CONFLICT(batch_id) DO NOTHING RETURNING batch_id`, b.ID, b.Scope.Date, b.Scope.Route, b.Scope.Province, b.Scope.District, b.Total, len(b.Pages), congestionpipeline.PolicyVersion, string(b.Policy)).Scan(&inserted)
	if errors.Is(e, pgx.ErrNoRows) {
		var total, count int
		var version string
		e = tx.QueryRow(ctx, `SELECT total_count,interpretation_version,(SELECT count(*) FROM public.congestion_observations o WHERE o.batch_id=b.batch_id) FROM public.congestion_batches b WHERE batch_id=$1`, b.ID).Scan(&total, &version, &count)
		if e != nil || total != b.Total || count != b.Total || version != congestionpipeline.PolicyVersion {
			return result, errors.New("existing batch conflict")
		}
		result.Status = "duplicate"
		return result, nil
	}
	if e != nil {
		return result, errors.New("batch insert failed")
	}
	for _, p := range b.Pages {
		if _, e = tx.Exec(ctx, `INSERT INTO public.congestion_pages(batch_id,page_no,http_status,record_count,response_sha256,collected_at,raw_response) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb)`, b.ID, p.Number, p.HTTP, len(p.Rows), p.SHA, p.At, string(p.Raw)); e != nil {
			return result, errors.New("page insert failed")
		}
		for i, raw := range p.Rows {
			var sequence *int
			v := congestionpipeline.Value(raw, "sttn_seq")
			if v != "" && v != "null" {
				n, err := strconv.Atoi(v)
				if err != nil {
					return result, errors.New("invalid stop sequence")
				}
				sequence = &n
			}
			var cgst any
			if v := congestionpipeline.Value(raw, "cgst"); v != "" && v != "null" {
				var n json.Number
				if json.Unmarshal([]byte(v), &n) != nil {
					return result, errors.New("invalid raw cgst")
				}
				cgst = n.String()
			}
			_, e = tx.Exec(ctx, `INSERT INTO public.congestion_observations(batch_id,page_no,row_no,service_date,stcis_route_id,ctpv_cd,sgg_cd,stcis_stop_id,stop_sequence,trip_ordinal,time_code,cgst_raw,raw_record) VALUES($1,$2,$3,$4::text::date,$5,$6,$7,$8,$9,$10,$11,$12::text::numeric,$13::jsonb)`, b.ID, p.Number, i+1, b.Scope.Date, b.Scope.Route, b.Scope.Province, b.Scope.District, congestionpipeline.Value(raw, "sttn_id"), sequence, congestionpipeline.Value(raw, "opr_trntm"), congestionpipeline.Value(raw, "tzon"), cgst, string(raw))
			if e != nil {
				return result, errors.New("observation insert failed")
			}
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return result, errors.New("ingest commit failed; inspect batch before retry")
	}
	result.Status = "inserted"
	return result, nil
}
