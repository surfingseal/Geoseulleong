// Package database owns the read-only application connection pool.
package database

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strconv"
	"time"
)

var ErrConfig = errors.New("database configuration invalid")
var ErrConnect = errors.New("database connection failed")

// Config uses DATABASE_URL or the standard PG* environment without logging either.
func Config() (*pgxpool.Config, error) {
	raw, e := deploymentDSN(os.Getenv("DATABASE_URL"))
	if e != nil {
		return nil, e
	}
	c, e := pgxpool.ParseConfig(raw)
	if e != nil {
		return nil, ErrConfig
	}
	if os.Getenv("DATABASE_URL") == "" && os.Getenv("PGDATABASE") == "" && os.Getenv("PGSERVICE") == "" {
		c.ConnConfig.Database = "geoseulleong"
	}
	max := 4
	if v := os.Getenv("DB_MAX_CONNS"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 16 {
			return nil, ErrConfig
		}
		max = n
	}
	c.MaxConns = int32(max)
	c.MinConns = 0
	c.MaxConnLifetime = 30 * time.Minute
	c.MaxConnIdleTime = 5 * time.Minute
	c.HealthCheckPeriod = time.Minute
	c.ConnConfig.ConnectTimeout = 5 * time.Second
	c.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	c.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	c.ConnConfig.RuntimeParams["application_name"] = "geoseulleong-read-api"
	return c, nil
}
func Open(ctx context.Context) (*pgxpool.Pool, error) {
	c, e := Config()
	if e != nil {
		return nil, e
	}
	return OpenConfig(ctx, c)
}
func OpenConfig(ctx context.Context, c *pgxpool.Config) (*pgxpool.Pool, error) {
	// Caller-supplied configs still receive the same read-only session policy.
	c = c.Copy()
	if c.ConnConfig.RuntimeParams == nil {
		c.ConnConfig.RuntimeParams = map[string]string{}
	}
	c.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	c.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	if c.ConnConfig.ConnectTimeout == 0 || c.ConnConfig.ConnectTimeout > 5*time.Second {
		c.ConnConfig.ConnectTimeout = 5 * time.Second
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pool, e := pgxpool.NewWithConfig(bounded, c)
	if e != nil {
		return nil, ErrConnect
	}
	if e = pool.Ping(bounded); e != nil {
		pool.Close()
		return nil, ErrConnect
	}
	return pool, nil
}
