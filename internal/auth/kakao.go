package auth

import (
	"context"
	"errors"
	"strings"
)

var ErrKakaoUnavailable = errors.New("Kakao login unavailable")
var ErrConsentRequired = errors.New("first signup requires service consents")

type KakaoLogin struct {
	AccessToken   string `json:"access_token"`
	TermsAgreed   bool   `json:"terms_agreed"`
	PrivacyAgreed bool   `json:"privacy_agreed"`
}

func (KakaoLogin) String() string      { return "Kakao login request (token omitted)" }
func (in KakaoLogin) GoString() string { return in.String() }

type KakaoIdentity struct{ UserID string }
type KakaoVerifier interface {
	Verify(context.Context, string) (KakaoIdentity, error)
}
type KakaoAccount struct {
	ProviderUserID               string
	User                         User
	Token                        Token
	TermsAgreed, PrivacyAgreed   bool
	TermsVersion, PrivacyVersion string
}
type KakaoStore interface {
	KakaoSession(context.Context, KakaoAccount) (User, error)
}

func validKakaoToken(token string) bool {
	if len(token) == 0 || len(token) > 2048 {
		return false
	}
	for _, c := range token {
		if c <= 32 || c > 126 {
			return false
		}
	}
	return true
}
func (s *Service) KakaoLogin(ctx context.Context, in KakaoLogin) (Session, error) {
	if !validKakaoToken(in.AccessToken) {
		return Session{}, ErrInvalid
	}
	if s.cfg.Kakao == nil {
		return Session{}, ErrKakaoUnavailable
	}
	store, ok := s.store.(KakaoStore)
	if !ok {
		return Session{}, ErrKakaoUnavailable
	}
	identity, e := s.cfg.Kakao.Verify(ctx, in.AccessToken)
	if e != nil {
		return Session{}, e
	}
	if identity.UserID == "" {
		return Session{}, ErrCredentials
	}
	now := s.cfg.Now().UTC()
	id := ID()
	// Keep nullable email empty in the public string DTO for backwards compatibility.
	// Never synthesize an email or match another account using a provider email.
	user := User{ID: id, Nickname: "카카오_" + strings.ReplaceAll(id, "-", "")[:16], Status: "active", CreatedAt: now}
	token, raw := newToken(id, now)
	user, e = store.KakaoSession(ctx, KakaoAccount{ProviderUserID: identity.UserID, User: user, Token: token, TermsAgreed: in.TermsAgreed, PrivacyAgreed: in.PrivacyAgreed, TermsVersion: s.cfg.TermsVersion, PrivacyVersion: s.cfg.PrivacyVersion})
	if e != nil {
		return Session{}, e
	}
	token.UserID = user.ID
	return s.session(user, token, raw)
}
