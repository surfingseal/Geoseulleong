package repository

import (
	"context"
	"encoding/json"
	"errors"
	"geoseulleong/internal/odsay"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type JourneySelections struct{ pool *pgxpool.Pool }

func NewJourneySelections(ctx context.Context, pool *pgxpool.Pool) (*JourneySelections, error) {
	var exists bool
	if pool.QueryRow(ctx, `SELECT to_regclass('public.journey_selections') IS NOT NULL`).Scan(&exists) != nil || !exists {
		return nil, ErrQuery
	}
	return &JourneySelections{pool}, nil
}
func (s *JourneySelections) Put(ctx context.Context, id string, value odsay.Selection, expires time.Time) error {
	raw, e := json.Marshal(value)
	if e != nil || len(raw) > 2<<20 {
		return ErrInvalidData
	}
	_, e = s.pool.Exec(ctx, `INSERT INTO journey_selections(search_id,payload,expires_at) VALUES($1,$2::jsonb,$3)`, id, string(raw), expires)
	if e != nil {
		return ErrQuery
	}
	return nil
}
func (s *JourneySelections) Fetch(ctx context.Context, id string) (odsay.Selection, bool, error) {
	var raw []byte
	e := s.pool.QueryRow(ctx, `SELECT payload FROM journey_selections WHERE search_id=$1 AND expires_at>clock_timestamp()`, id).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return odsay.Selection{}, false, nil
	}
	if e != nil {
		return odsay.Selection{}, false, ErrQuery
	}
	var value odsay.Selection
	if json.Unmarshal(raw, &value) != nil {
		return value, false, ErrInvalidData
	}
	return value, true, nil
}
func (s *JourneySelections) Recent(ctx context.Context) ([]odsay.Selection, error) {
	rows, e := s.pool.Query(ctx, `SELECT payload FROM journey_selections WHERE expires_at>clock_timestamp() ORDER BY created_at DESC LIMIT 64`)
	if e != nil {
		return nil, ErrQuery
	}
	defer rows.Close()
	out := []odsay.Selection{}
	for rows.Next() {
		var raw []byte
		var value odsay.Selection
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &value) != nil {
			return nil, ErrInvalidData
		}
		out = append(out, value)
	}
	if rows.Err() != nil {
		return nil, ErrQuery
	}
	return out, nil
}
