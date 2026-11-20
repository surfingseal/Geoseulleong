package repository

import (
	"context"
	"errors"
	"geoseulleong/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type Auth struct{ pool *pgxpool.Pool }

func NewAuth(p *pgxpool.Pool) *Auth { return &Auth{pool: p} }
func (a *Auth) Create(ctx context.Context, u auth.Account, key, terms, privacy string, now time.Time) error {
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `INSERT INTO users(user_id,email,password_hash,nickname,nickname_key,status,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, u.ID, u.Email, u.PasswordHash, u.Nickname, key, u.Status, u.CreatedAt)
	if e != nil {
		var pe *pgconn.PgError
		if errors.As(e, &pe) && pe.Code == "23505" {
			if pe.ConstraintName == "users_email_unique" {
				return auth.ErrEmailTaken
			}
			if pe.ConstraintName == "users_nickname_unique" {
				return auth.ErrNicknameTaken
			}
		}
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO user_consents(user_id,consent_type,version,agreed_at) VALUES($1,'terms',$2,$4),($1,'privacy',$3,$4)`, u.ID, terms, privacy, now)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (a *Auth) Find(ctx context.Context, email string) (auth.Account, error) {
	var u auth.Account
	e := a.pool.QueryRow(ctx, `SELECT user_id::text,email,password_hash,nickname,status,created_at FROM users WHERE email=$1`, email).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Nickname, &u.Status, &u.CreatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = auth.ErrCredentials
	}
	return u, e
}
func (a *Auth) NewSession(ctx context.Context, t auth.Token) error {
	tag, e := a.pool.Exec(ctx, `INSERT INTO refresh_tokens(token_id,family_id,user_id,token_hash,created_at,expires_at) SELECT $1,$2,user_id,$4,$5,$6 FROM users WHERE user_id=$3 AND status='active'`, t.ID, t.FamilyID, t.UserID, t.Hash, t.CreatedAt, t.ExpiresAt)
	if e == nil && tag.RowsAffected() != 1 {
		return auth.ErrCredentials
	}
	return e
}

// Every family mutation takes the same transaction-scoped lock, including logout
// and reuse detection. This prevents concurrent descendants escaping revocation.
func (a *Auth) mutate(ctx context.Context, hash []byte) (pgx.Tx, string, error) {
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		return nil, "", e
	}
	var family string
	e = tx.QueryRow(ctx, `SELECT family_id::text FROM refresh_tokens WHERE token_hash=$1`, hash).Scan(&family)
	if e != nil {
		tx.Rollback(ctx)
		if errors.Is(e, pgx.ErrNoRows) {
			e = auth.ErrCredentials
		}
		return nil, "", e
	}
	_, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "auth-family:"+family)
	if e != nil {
		tx.Rollback(ctx)
		return nil, "", e
	}
	return tx, family, nil
}
func (a *Auth) Rotate(ctx context.Context, hash []byte, next auth.Token, now time.Time) (auth.User, time.Time, error) {
	var u auth.User
	tx, family, e := a.mutate(ctx, hash)
	if e != nil {
		return u, time.Time{}, e
	}
	defer tx.Rollback(ctx)
	var old string
	var expiry time.Time
	var revoked *time.Time
	e = tx.QueryRow(ctx, `SELECT t.token_id::text,t.expires_at,t.revoked_at,u.user_id::text,u.email,u.nickname,u.status,u.created_at FROM refresh_tokens t JOIN users u USING(user_id) WHERE t.token_hash=$1 FOR UPDATE OF t`, hash).Scan(&old, &expiry, &revoked, &u.ID, &u.Email, &u.Nickname, &u.Status, &u.CreatedAt)
	if e != nil {
		return u, expiry, e
	}
	if revoked != nil {
		_, e = tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at=$2,revoke_reason='reuse' WHERE family_id=$1 AND revoked_at IS NULL`, family, now)
		if e != nil {
			return u, expiry, e
		}
		if e = tx.Commit(ctx); e != nil {
			return u, expiry, e
		}
		return auth.User{}, expiry, auth.ErrCredentials
	}
	if !now.Before(expiry) || u.Status != "active" {
		return auth.User{}, expiry, auth.ErrCredentials
	}
	_, e = tx.Exec(ctx, `INSERT INTO refresh_tokens(token_id,family_id,user_id,token_hash,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, next.ID, family, u.ID, next.Hash, now, expiry)
	if e != nil {
		return u, expiry, e
	}
	_, e = tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at=$2,revoke_reason='rotated',replaced_by=$3 WHERE token_id=$1`, old, now, next.ID)
	if e != nil {
		return u, expiry, e
	}
	e = tx.Commit(ctx)
	return u, expiry, e
}
func (a *Auth) Logout(ctx context.Context, hash []byte, now time.Time) error {
	tx, family, e := a.mutate(ctx, hash)
	if errors.Is(e, auth.ErrCredentials) {
		return nil
	}
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at=$2,revoke_reason='logout' WHERE family_id=$1 AND revoked_at IS NULL`, family, now)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (a *Auth) Current(ctx context.Context, uid, sid string, now time.Time) (auth.User, error) {
	var u auth.User
	e := a.pool.QueryRow(ctx, `SELECT u.user_id::text,u.email,u.nickname,u.status,u.created_at FROM users u JOIN refresh_tokens t USING(user_id) WHERE u.user_id::text=$1 AND t.token_id::text=$2 AND u.status='active' AND t.revoked_at IS NULL AND t.expires_at>$3`, uid, sid, now).Scan(&u.ID, &u.Email, &u.Nickname, &u.Status, &u.CreatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = auth.ErrCredentials
	}
	return u, e
}
