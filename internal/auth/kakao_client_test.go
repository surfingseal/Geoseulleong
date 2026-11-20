package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestKakaoClientVerification(t *testing.T) {
	for _, tc := range []struct {
		name, info, user  string
		status, wantCalls int
		want              error
	}{
		{"valid-no-email", `{"id":9007199254740993,"app_id":1652139,"expires_in":3600}`, `{"id":9007199254740993}`, 200, 2, nil},
		{"wrong-app", `{"id":123,"app_id":999,"expires_in":3600}`, `{"id":123}`, 200, 1, ErrCredentials},
		{"expired", `{"id":123,"app_id":1652139,"expires_in":0}`, `{"id":123}`, 200, 1, ErrCredentials},
		{"missing-expiry", `{"id":123,"app_id":1652139}`, `{"id":123}`, 200, 1, ErrCredentials},
		{"identity-mismatch", `{"id":123,"app_id":1652139,"expires_in":3600}`, `{"id":456}`, 200, 2, ErrCredentials},
		{"preregistered", `{"id":123,"app_id":1652139,"expires_in":3600}`, `{"id":123,"has_signed_up":false}`, 200, 2, ErrCredentials},
		{"unauthorized", `{"code":-401}`, `{}`, 401, 1, ErrCredentials},
		{"temporary", `{"code":-1}`, `{}`, 400, 1, ErrKakaoUnavailable},
		{"server-error", `{}`, `{}`, 500, 1, ErrKakaoUnavailable},
		{"invalid-json", `invalid`, `{}`, 200, 1, ErrKakaoUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer synthetic-kakao-token" || strings.Contains(r.URL.RawQuery, "synthetic-kakao-token") {
					t.Error("unsafe credential handling")
				}
				if r.URL.Path == "/v1/user/access_token_info" {
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.info)
				} else if r.URL.Path == "/v2/user/me" {
					if strings.Contains(r.URL.Query().Get("property_keys"), "email") {
						t.Error("unneeded email scope")
					}
					fmt.Fprint(w, tc.user)
				} else {
					t.Error("unexpected endpoint")
				}
			}))
			defer server.Close()
			client, e := NewKakaoClient(1652139)
			if e != nil {
				t.Fatal(e)
			}
			client.baseURL = server.URL
			identity, e := client.Verify(context.Background(), "synthetic-kakao-token")
			if !errors.Is(e, tc.want) || calls.Load() != int32(tc.wantCalls) {
				t.Fatalf("verification result=%v calls=%d", e, calls.Load())
			}
			if e == nil && identity.UserID != "9007199254740993" {
				t.Fatal("ID precision loss")
			}
			if e != nil && strings.Contains(e.Error(), "synthetic-kakao-token") {
				t.Fatal("token leaked")
			}
		})
	}
}
func TestKakaoClientNeverFollowsRedirect(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer server.Close()
	client, _ := NewKakaoClient(1652139)
	client.baseURL = server.URL
	if _, e := client.Verify(context.Background(), "synthetic-kakao-token"); !errors.Is(e, ErrKakaoUnavailable) || leaked.Load() != 0 {
		t.Fatal("redirect followed")
	}
}
func TestKakaoInvalidTokenNoHTTP(t *testing.T) {
	client, _ := NewKakaoClient(1652139)
	for _, token := range []string{"", "bad\r\nheader", "with space", strings.Repeat("a", 2049)} {
		if _, e := client.Verify(context.Background(), token); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid token accepted")
		}
	}
	if fmt.Sprintf("%+v", KakaoLogin{AccessToken: "do-not-log-this"}) != "Kakao login request (token omitted)" {
		t.Fatal("unsafe request formatting")
	}
}
