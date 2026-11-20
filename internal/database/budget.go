package database

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"time"
)

// OpenBudget is a separate writable pool for external_api_budgets only.
func OpenBudget(ctx context.Context) (*pgxpool.Pool, error) {
	raw := os.Getenv("BUDGET_DATABASE_URL")
	if raw == "" && os.Getenv("DYNO") != "" {
		raw = os.Getenv("DATABASE_URL")
	}
	if raw == "" {
		return nil, ErrConfig
	}
	raw, e := deploymentDSN(raw)
	if e != nil {
		return nil, e
	}
	c, e := pgxpool.ParseConfig(raw)
	if e != nil {
		return nil, ErrConfig
	}
	c.MaxConns = 2
	c.ConnConfig.ConnectTimeout = 5 * time.Second
	c.ConnConfig.RuntimeParams["application_name"] = "geoseulleong-api-budget"
	c.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, e := pgxpool.NewWithConfig(bounded, c)
	if e != nil {
		return nil, ErrConnect
	}
	var exists bool
	e = p.QueryRow(bounded, `SELECT to_regclass('public.external_api_budgets') IS NOT NULL`).Scan(&exists)
	if e != nil || !exists {
		p.Close()
		return nil, ErrConnect
	}
	return p, nil
}
