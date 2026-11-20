package database

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"time"
)

// OpenAuth is explicit and separate from the always-read-only transport pool.
// No fallback to DATABASE_URL, no schema changes and no writes during startup.
func OpenAuth(ctx context.Context) (*pgxpool.Pool, error) {
	raw := os.Getenv("AUTH_DATABASE_URL")
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
	c.MaxConns = 4
	c.MinConns = 0
	c.MaxConnIdleTime = 5 * time.Minute
	c.MaxConnLifetime = 30 * time.Minute
	c.ConnConfig.ConnectTimeout = 5 * time.Second
	c.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	c.ConnConfig.RuntimeParams["lock_timeout"] = "3000"
	c.ConnConfig.RuntimeParams["application_name"] = "geoseulleong-auth"
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		return nil, ErrConnect
	}
	if e = p.Ping(ctx); e != nil {
		p.Close()
		return nil, ErrConnect
	}
	return p, nil
}
