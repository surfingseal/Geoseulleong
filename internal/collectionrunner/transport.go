package collectionrunner

import (
	"context"
	"errors"
	"fmt"
	"geoseulleong/internal/collectionpreview"
	rc "geoseulleong/internal/routecongestion"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"path/filepath"
)

// Live additionally requires a human-approved exact config hash. Keys never enter
// manifests/checkpoints/logs. The CLI verifies the read-only internal DB binding first.
func (p *Permit) Live(ctx context.Context, pool *pgxpool.Pool, key, approvedHash string) (*rc.Client, error) {
	if p == nil || p.Config.FixtureOnly || approvedHash == "" || approvedHash != p.Hash {
		return nil, errors.New("explicit reviewed live authorization hash required")
	}
	fresh, e := Prepare(p.Config)
	if e != nil || fresh.Hash != p.Hash {
		return nil, errors.New("permit mutated")
	}
	if pool == nil {
		return nil, errors.New("read-only internal identity verification required")
	}
	for _, id := range fresh.identities {
		if e = id.VerifyDB(ctx, pool); e != nil {
			return nil, e
		}
	}
	scopes := []rc.Scope{}
	for s := range fresh.scopes {
		scopes = append(scopes, s)
	}
	return rc.NewScoped(key, scopes)
}

// Files reads exact saved provider envelopes. It never synthesizes absent data.
type Files struct{ Root string }

func (f Files) Get(ctx context.Context, s rc.Scope, page int) ([]byte, int, error) {
	if e := ctx.Err(); e != nil {
		return nil, 0, e
	}
	// Scope values were validated by Prepare; hashing avoids path interpolation.
	path := filepath.Join(f.Root, FixtureName(s, page))
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, 0, errors.New("saved response missing")
	}
	return b, 200, nil
}

func FixtureName(s rc.Scope, page int) string {
	return fmt.Sprintf("%s-page-%04d.json", Digest([]byte(collectionpreview.ScopeKey(s))), page)
}
