package repository

import (
	"context"
	"errors"
	"geoseulleong/internal/auth"
	"github.com/jackc/pgx/v5"
)

// The provider identity, user, consents and refresh session commit atomically.
// Locking the exact verified provider ID prevents two concurrent first logins
// from creating separate users. Provider email is deliberately not an input.
func (a *Auth) KakaoSession(ctx context.Context, in auth.KakaoAccount) (auth.User, error) {
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		return auth.User{}, ErrQuery
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "kakao-user:"+in.ProviderUserID); e != nil {
		return auth.User{}, ErrQuery
	}
	var user auth.User
	e = tx.QueryRow(ctx, `SELECT u.user_id::text,coalesce(u.email,''),u.nickname,u.status,u.created_at FROM oauth_identities i JOIN users u USING(user_id) WHERE i.provider='kakao' AND i.provider_user_id=$1 FOR UPDATE OF u`, in.ProviderUserID).Scan(&user.ID, &user.Email, &user.Nickname, &user.Status, &user.CreatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		if !in.TermsAgreed || !in.PrivacyAgreed {
			return auth.User{}, auth.ErrConsentRequired
		}
		user = in.User
		_, e = tx.Exec(ctx, `INSERT INTO users(user_id,email,password_hash,nickname,nickname_key,status,created_at) VALUES($1,NULL,NULL,$2,$2,'active',$3)`, user.ID, user.Nickname, user.CreatedAt)
		if e != nil {
			return auth.User{}, ErrQuery
		}
		_, e = tx.Exec(ctx, `INSERT INTO oauth_identities(provider,provider_user_id,user_id) VALUES('kakao',$1,$2)`, in.ProviderUserID, user.ID)
		if e != nil {
			return auth.User{}, ErrQuery
		}
		_, e = tx.Exec(ctx, `INSERT INTO user_consents(user_id,consent_type,version,agreed_at) VALUES($1,'terms',$2,$4),($1,'privacy',$3,$4)`, user.ID, in.TermsVersion, in.PrivacyVersion, in.User.CreatedAt)
		if e != nil {
			return auth.User{}, ErrQuery
		}
	} else if e != nil {
		return auth.User{}, ErrQuery
	}
	if user.Status != "active" {
		return auth.User{}, auth.ErrCredentials
	}
	t := in.Token
	_, e = tx.Exec(ctx, `INSERT INTO refresh_tokens(token_id,family_id,user_id,token_hash,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, t.ID, t.FamilyID, user.ID, t.Hash, t.CreatedAt, t.ExpiresAt)
	if e != nil {
		return auth.User{}, ErrQuery
	}
	if tx.Commit(ctx) != nil {
		return auth.User{}, ErrQuery
	}
	return user, nil
}
