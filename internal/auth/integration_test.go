package auth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"geoseulleong/internal/auth"
	"geoseulleong/internal/httpapi"
	"geoseulleong/internal/repository"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func isolated(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("AUTH_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("AUTH_TEST_DATABASE_URL absent; no live writes")
	}
	c, e := pgxpool.ParseConfig(raw)
	if e != nil {
		t.Fatal("invalid test config")
	}
	if c.ConnConfig.Database != "geoseulleong_auth_test" || !filepath.IsAbs(c.ConnConfig.Host) {
		t.Fatal("test DB must be dedicated temp Unix socket")
	}
	host, e := filepath.EvalSymlinks(c.ConnConfig.Host)
	if e != nil {
		t.Fatal(e)
	}
	root := filepath.Dir(host)
	if !strings.HasPrefix(root, "/private/tmp/geoseulleong-auth.") {
		t.Fatal("refusing nonisolated socket")
	}
	ctx := context.Background()
	pool, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	var data, db string
	if e = pool.QueryRow(ctx, "SELECT current_setting('data_directory'),current_database()").Scan(&data, &db); e != nil {
		t.Fatal(e)
	}
	actual, e := filepath.EvalSymlinks(data)
	if e != nil || filepath.Dir(actual) != root || db != "geoseulleong_auth_test" {
		pool.Close()
		t.Fatal("test cluster identity rejected")
	}
	schema := "auth_test_" + strings.ReplaceAll(auth.ID(), "-", "")
	if _, e = pool.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	pool.Close()
	c.ConnConfig.RuntimeParams["search_path"] = schema
	c.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	pool, e = pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); pool.Close() })
	sql, e := os.ReadFile("../../migrations/006_auth.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, string(sql)); e != nil {
		t.Fatal(e)
	}
	migration, e := os.ReadFile("../../migrations/009_kakao_login.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, string(migration)); e != nil {
		t.Fatal(e)
	}
	return pool
}
func TestAuthSQLAndHTTP(t *testing.T) {
	p := isolated(t)
	ctx := context.Background()
	repo := repository.NewAuth(p)
	now := time.Now().UTC().Truncate(time.Second)
	secret := make([]byte, 32)
	rand.Read(secret)
	service, e := auth.New(repo, auth.Config{Secret: secret, TermsVersion: "test-terms-v1", PrivacyVersion: "test-privacy-v1", Cost: 4, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	input := func(email, nick string) auth.Signup {
		return auth.Signup{Email: email, Nickname: nick, Password: "Synthetic9!", PasswordConfirm: "Synthetic9!", TermsAgreed: true, PrivacyAgreed: true}
	}
	signup := input(" person@example.com ", "테스터")
	u, e := service.Signup(ctx, signup)
	if e != nil || u.Email != "person@example.com" {
		t.Fatal(e)
	}
	var consents int
	p.QueryRow(ctx, "SELECT count(*) FROM user_consents WHERE user_id=$1", u.ID).Scan(&consents)
	if consents != 2 {
		t.Fatal("consents not atomic")
	}
	a, e := repo.Find(ctx, u.Email)
	if e != nil || a.PasswordHash == signup.Password || !strings.HasPrefix(a.PasswordHash, "$2") {
		t.Fatal("password not hashed")
	}
	if _, e = service.Signup(ctx, input("PERSON@example.com", "다른닉")); !errors.Is(e, auth.ErrEmailTaken) {
		t.Fatal(e)
	}
	if _, e = service.Signup(ctx, input("different@example.com", "테스터")); !errors.Is(e, auth.ErrNicknameTaken) {
		t.Fatal(e)
	}
	for _, mutate := range []func(*auth.Signup){func(x *auth.Signup) { x.Password = "bad" }, func(x *auth.Signup) { x.PasswordConfirm = "bad" }, func(x *auth.Signup) { x.TermsAgreed = false }, func(x *auth.Signup) { x.PrivacyAgreed = false }, func(x *auth.Signup) { x.Nickname = "공 백" }} {
		x := input("invalid@example.com", "불가닉")
		mutate(&x)
		if _, e = service.Signup(ctx, x); !errors.Is(e, auth.ErrInvalid) {
			t.Fatal("validation", e)
		}
	}
	if _, e = service.Signup(ctx, input("case1@example.com", "CaseNick")); e != nil {
		t.Fatal(e)
	}
	if _, e = service.Signup(ctx, input("case2@example.com", "casenick")); !errors.Is(e, auth.ErrNicknameTaken) {
		t.Fatal("nickname case uniqueness", e)
	}
	// A failing consent INSERT must roll back the preceding user INSERT.
	broken := auth.Account{User: auth.User{ID: auth.ID(), Email: "rollback@example.com", Nickname: "롤백", Status: "active", CreatedAt: now}, PasswordHash: a.PasswordHash}
	if e = repo.Create(ctx, broken, "롤백", strings.Repeat("x", 65), "v1", now); e == nil {
		t.Fatal("expected constraint failure")
	}
	var n int
	p.QueryRow(ctx, "SELECT count(*) FROM users WHERE email='rollback@example.com'").Scan(&n)
	if n != 0 {
		t.Fatal("signup rollback failed")
	}
	// Database uniqueness, including concurrent normalized emails and nickname keys.
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := service.Signup(ctx, input("race@example.com", "동시닉")); e == nil {
				ok.Add(1)
			} else if !errors.Is(e, auth.ErrEmailTaken) && !errors.Is(e, auth.ErrNicknameTaken) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatal("duplicate concurrent signup")
	}
	if _, e = service.Login(ctx, auth.Login{Email: u.Email, Password: "wrong"}); !errors.Is(e, auth.ErrCredentials) {
		t.Fatal(e)
	}
	if _, e = service.Login(ctx, auth.Login{Email: "absent@example.com", Password: "wrong"}); !errors.Is(e, auth.ErrCredentials) {
		t.Fatal(e)
	}
	login := func() auth.Session {
		s, e := service.Login(ctx, auth.Login{Email: u.Email, Password: signup.Password})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	s := login()
	if _, e = service.Me(ctx, s.AccessToken); e != nil {
		t.Fatal(e)
	}
	if _, e = service.Me(ctx, s.AccessToken+"x"); !errors.Is(e, auth.ErrCredentials) {
		t.Fatal("forged token accepted")
	}
	expired, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": "geoseulleong", "aud": "geoseulleong-api", "sub": u.ID, "sid": auth.ID(), "exp": now.Add(-time.Second).Unix()}).SignedString(secret)
	if _, e = service.Me(ctx, expired); !errors.Is(e, auth.ErrCredentials) {
		t.Fatal("expired access accepted")
	}
	rotated, e := service.Refresh(ctx, s.RefreshToken)
	if e != nil || rotated.RefreshToken == s.RefreshToken {
		t.Fatal("rotation", e)
	}
	if _, e = service.Me(ctx, s.AccessToken); !errors.Is(e, auth.ErrCredentials) {
		t.Fatal("old access not revoked")
	}
	if _, e = service.Me(ctx, rotated.AccessToken); e != nil {
		t.Fatal(e)
	}
	if _, e = service.Refresh(ctx, s.RefreshToken); !errors.Is(e, auth.ErrCredentials) {
		t.Fatal("refresh replay accepted")
	}
	if _, e = service.Refresh(ctx, rotated.RefreshToken); !errors.Is(e, auth.ErrCredentials) {
		t.Fatal("family not revoked on replay")
	}
	if _, e = service.Me(ctx, rotated.AccessToken); !errors.Is(e, auth.ErrCredentials) {
		t.Fatal("family access not revoked")
	}
	s = login()
	if e = service.Logout(ctx, s.RefreshToken); e != nil {
		t.Fatal(e)
	}
	if _, e = service.Me(ctx, s.AccessToken); !errors.Is(e, auth.ErrCredentials) {
		t.Fatal("logout access active")
	}
	if _, e = service.Refresh(ctx, s.RefreshToken); !errors.Is(e, auth.ErrCredentials) {
		t.Fatal("logout refresh active")
	}
	s = login()
	now = now.Add(31 * 24 * time.Hour)
	if _, e = service.Refresh(ctx, s.RefreshToken); !errors.Is(e, auth.ErrCredentials) {
		t.Fatal("expired refresh accepted")
	}
	now = now.Add(-31 * 24 * time.Hour)
	// Concurrent rotation: one succeeds, replay detection revokes its family.
	s = login()
	var rotatedCount atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := service.Refresh(ctx, s.RefreshToken); e == nil {
				rotatedCount.Add(1)
			} else if !errors.Is(e, auth.ErrCredentials) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if rotatedCount.Load() != 1 {
		t.Fatal("concurrent rotation")
	}
	handler := httpapi.WithAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }), service)
	request := func(method, path string, body any, token string) *httptest.ResponseRecorder {
		var b bytes.Buffer
		if body != nil {
			json.NewEncoder(&b).Encode(body)
		}
		r := httptest.NewRequest(method, path, &b)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/v1/users/me", nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request("POST", "/api/v1/auth/signup", input("http@example.com", "HTTP닉"), "")
	if w.Code != 201 || strings.Contains(w.Body.String(), "password") {
		t.Fatal(w.Code)
	}
	w = request("POST", "/api/v1/auth/login", auth.Login{Email: u.Email, Password: signup.Password}, "")
	var session auth.Session
	json.Unmarshal(w.Body.Bytes(), &session)
	if w.Code != 200 || session.AccessToken == "" {
		t.Fatal(w.Code)
	}
	if w = request("GET", "/api/v1/users/me", nil, session.AccessToken); w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = request("POST", "/api/v1/auth/refresh", auth.Refresh{RefreshToken: session.RefreshToken}, "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &session)
	if w = request("POST", "/api/v1/auth/logout", auth.Refresh{RefreshToken: session.RefreshToken}, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w = request("GET", "/api/v1/users/me", nil, session.AccessToken); w.Code != 401 {
		t.Fatal(w.Code)
	}
	var unknown, wrong string
	for i := 0; i < 6; i++ {
		w = request("POST", "/api/v1/auth/login", auth.Login{Email: "missing@example.com", Password: "wrong"}, "")
		if i == 0 {
			unknown = w.Body.String()
		}
		if i == 5 && w.Code != 429 {
			t.Fatal("login rate limit", w.Code)
		}
	}
	w = request("POST", "/api/v1/auth/login", auth.Login{Email: u.Email, Password: "wrong"}, "")
	wrong = w.Body.String()
	if unknown != wrong {
		t.Fatal("account existence leaked")
	}
	if w = request("POST", "/api/v1/auth/signup", map[string]any{"unknown": true}, ""); w.Code != 400 {
		t.Fatal("unknown fields")
	}
}
