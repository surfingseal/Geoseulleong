package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"geoseulleong/internal/auth"
	"geoseulleong/internal/httpapi"
	"geoseulleong/internal/repository"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type kakaoStub struct{}

func (kakaoStub) Verify(_ context.Context, token string) (auth.KakaoIdentity, error) {
	switch token {
	case "synthetic-existing-email":
		return auth.KakaoIdentity{UserID: "111"}, nil
	case "synthetic-concurrent":
		return auth.KakaoIdentity{UserID: "222"}, nil
	case "synthetic-missing-consent":
		return auth.KakaoIdentity{UserID: "333"}, nil
	}
	return auth.KakaoIdentity{}, auth.ErrCredentials
}
func TestKakaoSQLHTTPAndSessionCompatibility(t *testing.T) {
	pool := isolated(t)
	ctx := context.Background()
	repo := repository.NewAuth(pool)
	svc, e := auth.New(repo, auth.Config{Secret: []byte(strings.Repeat("s", 32)), TermsVersion: "test-v1", PrivacyVersion: "test-v1", Cost: 4, Kakao: kakaoStub{}})
	if e != nil {
		t.Fatal(e)
	}
	local, e := svc.Signup(ctx, auth.Signup{Email: "person@example.com", Nickname: "일반사용자", Password: "Synthetic9!", PasswordConfirm: "Synthetic9!", TermsAgreed: true, PrivacyAgreed: true})
	if e != nil {
		t.Fatal(e)
	}
	in := auth.KakaoLogin{AccessToken: "synthetic-existing-email", TermsAgreed: true, PrivacyAgreed: true}
	first, e := svc.KakaoLogin(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	if first.User.ID == local.ID || first.User.Email != "" || first.AccessToken == in.AccessToken {
		t.Fatal("account auto-linked or invalid session")
	}
	var nulls bool
	var consents int
	if e = pool.QueryRow(ctx, `SELECT email IS NULL AND password_hash IS NULL FROM users WHERE user_id=$1`, first.User.ID).Scan(&nulls); e != nil || !nulls {
		t.Fatal("OAuth needs no local password/email")
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM user_consents WHERE user_id=$1`, first.User.ID).Scan(&consents); e != nil || consents != 2 {
		t.Fatal("consents missing")
	}
	again, e := svc.KakaoLogin(ctx, auth.KakaoLogin{AccessToken: in.AccessToken})
	if e != nil || again.User.ID != first.User.ID {
		t.Fatal("returning user duplicated", e)
	}
	if _, e = svc.Me(ctx, first.AccessToken); e != nil {
		t.Fatal(e)
	}
	rotated, e := svc.Refresh(ctx, first.RefreshToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Refresh(ctx, first.RefreshToken); !errors.Is(e, auth.ErrCredentials) {
		t.Fatal("refresh reuse accepted")
	}
	if _, e = svc.Me(ctx, rotated.AccessToken); !errors.Is(e, auth.ErrCredentials) {
		t.Fatal("reused family not revoked")
	}
	if e = svc.Logout(ctx, again.RefreshToken); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Me(ctx, again.AccessToken); !errors.Is(e, auth.ErrCredentials) {
		t.Fatal("logout failed")
	}
	var wg sync.WaitGroup
	ids := make(chan string, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := svc.KakaoLogin(ctx, auth.KakaoLogin{AccessToken: "synthetic-concurrent", TermsAgreed: true, PrivacyAgreed: true})
			if e != nil {
				t.Error(e)
				return
			}
			ids <- v.User.ID
		}()
	}
	wg.Wait()
	close(ids)
	chosen := ""
	n := 0
	for id := range ids {
		n++
		if chosen != "" && chosen != id {
			t.Fatal("concurrent duplicate account")
		}
		chosen = id
	}
	if n != 6 {
		t.Fatal("concurrent login failed")
	}
	var count int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM oauth_identities WHERE provider='kakao' AND provider_user_id='222'`).Scan(&count); e != nil || count != 1 {
		t.Fatal("duplicate identity")
	}
	if _, e = svc.KakaoLogin(ctx, auth.KakaoLogin{AccessToken: "synthetic-missing-consent"}); !errors.Is(e, auth.ErrConsentRequired) {
		t.Fatal("silent consent")
	}
	candidate := auth.User{ID: auth.ID(), Nickname: "롤백전용", Status: "active", CreatedAt: time.Now().UTC()}
	_, e = repo.KakaoSession(ctx, auth.KakaoAccount{ProviderUserID: "444", User: candidate, TermsAgreed: true, PrivacyAgreed: true, TermsVersion: strings.Repeat("a", 65), PrivacyVersion: "v1"})
	if e == nil {
		t.Fatal("bad consent insert accepted")
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE user_id=$1`, candidate.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("user not rolled back")
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM oauth_identities WHERE provider_user_id='333'`).Scan(&count); e != nil || count != 0 {
		t.Fatal("consent rejection created user")
	}
	handler := httpapi.WithAuth(http.NotFoundHandler(), svc)
	request := func(path string, in any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(in)
		r := httptest.NewRequest("POST", path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request("/api/v1/auth/kakao", in)
	if w.Code != 200 || strings.Contains(w.Body.String(), in.AccessToken) || strings.Contains(w.Body.String(), "password_hash") {
		t.Fatal("HTTP OAuth contract", w.Code)
	}
	var session auth.Session
	if json.Unmarshal(w.Body.Bytes(), &session) != nil || session.AccessToken == "" {
		t.Fatal("missing JWT")
	}
	if _, e = svc.Me(ctx, session.AccessToken); e != nil {
		t.Fatal("me failed", e)
	}
	if w = request("/api/v1/auth/kakao", auth.KakaoLogin{}); w.Code != 400 {
		t.Fatal("empty token", w.Code)
	}
	if w = request("/api/v1/auth/kakao", auth.KakaoLogin{AccessToken: "invalid-token"}); w.Code != 401 {
		t.Fatal("invalid token", w.Code)
	}
	if w = request("/api/v1/auth/login", auth.Login{Email: local.Email, Password: "Synthetic9!"}); w.Code != 200 {
		t.Fatal("local login regression", w.Code)
	}
	t.Log("mock Kakao + SQL: no-email account, no auto-merge, concurrent identity, JWT/refresh/logout and HTTP passed")
}
