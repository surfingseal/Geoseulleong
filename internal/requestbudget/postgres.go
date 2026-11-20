// Package requestbudget reserves external calls durably before any HTTP transmission.
package requestbudget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

var ErrUnavailable = errors.New("persistent API budget unavailable")
var ErrExhausted = errors.New("persistent API budget exhausted")

type Store struct{ Pool *pgxpool.Pool }
type Limits struct{ Daily, Total, ExternalReserve int }

// Scope binds provider and credential without persisting the credential itself.
func Scope(provider, key string) string {
	h := sha256.Sum256([]byte(key))
	return provider + ":" + hex.EncodeToString(h[:])
}

// Rows are explicitly initialized during migration/import. A missing ledger is
// an error, never a silent reset. Counter updates commit before the caller sends.
func (s Store) Reserve(ctx context.Context, scope string, limits Limits) error {
	if s.Pool == nil || scope == "" || limits.Daily < 1 || limits.Total < 1 || limits.ExternalReserve < 0 || limits.ExternalReserve >= limits.Daily {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return ErrUnavailable
	}
	defer tx.Rollback(ctx)
	var day string
	var daily, total int
	var last *time.Time
	var now time.Time
	e = tx.QueryRow(ctx, `SELECT budget_day::text,daily_reserved,total_reserved,last_reserved_at,clock_timestamp() FROM external_api_budgets WHERE scope=$1 FOR UPDATE`, scope).Scan(&day, &daily, &total, &last, &now)
	if e != nil {
		return ErrUnavailable
	}
	zone := time.FixedZone("KST", 9*60*60)
	today := now.In(zone).Format("2006-01-02")
	if day > today {
		return ErrUnavailable
	}
	if day != today {
		daily = 0
	}
	if daily >= limits.Daily-limits.ExternalReserve || total >= limits.Total {
		return ErrExhausted
	}
	if last != nil {
		if delay := last.Add(2 * time.Second).Sub(now); delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ErrUnavailable
			case <-timer.C:
			}
		}
	}
	_, e = tx.Exec(ctx, `UPDATE external_api_budgets SET budget_day=(clock_timestamp() AT TIME ZONE 'Asia/Seoul')::date,daily_reserved=$2,total_reserved=total_reserved+1,last_reserved_at=clock_timestamp() WHERE scope=$1`, scope, daily+1)
	if e != nil || tx.Commit(ctx) != nil {
		return ErrUnavailable
	}
	return nil
}
