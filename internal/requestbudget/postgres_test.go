package requestbudget

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
)

func TestMissingStoreFailsClosed(t *testing.T) {
	if (Store{}).Reserve(context.Background(), "scope", Limits{Daily: 1, Total: 1}) != ErrUnavailable {
		t.Fatal("missing storage allowed")
	}
}
func TestPostgresDurability(t *testing.T) {
	raw := os.Getenv("TEST_BUDGET_DATABASE_URL")
	if raw == "" {
		t.Skip("explicit isolated database required")
	}
	cfg, e := pgxpool.ParseConfig(raw)
	if e != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "geoseulleong_budget_test_") {
		t.Fatal("isolated database required")
	}
	open := func() *pgxpool.Pool {
		p, e := pgxpool.NewWithConfig(context.Background(), cfg.Copy())
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	ctx := context.Background()
	p := open()
	scope := Scope("odsay", "synthetic-test-key")
	_, e = p.Exec(ctx, `INSERT INTO external_api_budgets(scope,budget_day,daily_reserved,total_reserved) VALUES($1,(now() AT TIME ZONE 'Asia/Seoul')::date,2,2)`, scope)
	if e != nil {
		t.Fatal(e)
	}
	limits := Limits{Daily: 3, Total: 3}
	if e = (Store{p}).Reserve(ctx, scope, limits); e != nil {
		t.Fatal(e)
	}
	p.Close()
	p = open()
	defer p.Close()
	if e = (Store{p}).Reserve(ctx, scope, limits); e != ErrExhausted {
		t.Fatal("restart reset counters", e)
	}
	if e = (Store{p}).Reserve(ctx, "missing", limits); e != ErrUnavailable {
		t.Fatal("missing ledger reset")
	}
	var daily, total int
	if e = p.QueryRow(ctx, `SELECT daily_reserved,total_reserved FROM external_api_budgets WHERE scope=$1`, scope).Scan(&daily, &total); e != nil || daily != 3 || total != 3 {
		t.Fatal("counter mismatch")
	}
}
