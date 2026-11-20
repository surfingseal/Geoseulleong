package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"geoseulleong/internal/apidocs"
	"geoseulleong/internal/auth"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type limitEntry struct {
	count int
	until time.Time
}
type authLimiter struct {
	mu      sync.Mutex
	entries map[string]limitEntry
}

func (l *authLimiter) allow(key string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.entries) >= 4096 {
		for k, v := range l.entries {
			if !now.Before(v.until) {
				delete(l.entries, k)
			}
		}
	}
	v, exists := l.entries[key]
	if !exists && len(l.entries) >= 4096 {
		return false
	}
	if !now.Before(v.until) {
		v = limitEntry{until: now.Add(5 * time.Minute)}
	}
	if v.count >= max {
		return false
	}
	v.count++
	l.entries[key] = v
	return true
}

// WithAuth adds opt-in auth without changing existing route contracts.
func WithAuth(base http.Handler, s *auth.Service) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/swagger/", apidocs.Handler())
	limiter := &authLimiter{entries: map[string]limitEntry{}}
	wrap := func(fn func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			if s == nil {
				failure(w, 503, "auth_disabled", "인증 기능이 비활성화되어 있습니다.")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			defer cancel()
			fn(w, r.WithContext(ctx))
		}
	}
	decode := func(w http.ResponseWriter, r *http.Request, v any) bool {
		typ, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || typ != "application/json" {
			failure(w, 400, "invalid_input", "JSON 요청이 필요합니다.")
			return false
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if e = d.Decode(v); e != nil {
			failure(w, 400, "invalid_input", "입력 형식을 확인해 주세요.")
			return false
		}
		if e = d.Decode(&struct{}{}); e != io.EOF {
			failure(w, 400, "invalid_input", "JSON 객체 하나만 허용합니다.")
			return false
		}
		return true
	}
	result := func(w http.ResponseWriter, status int, v any, e error) {
		if e == nil {
			write(w, status, v)
			return
		}
		switch {
		case errors.Is(e, auth.ErrKakaoUnavailable):
			failure(w, 503, "kakao_login_unavailable", "카카오 인증 설정 또는 일시적인 연결 상태를 확인해 주세요.")
		case errors.Is(e, auth.ErrConsentRequired):
			failure(w, 400, "consent_required", "최초 가입에는 이용약관과 개인정보 수집·이용 동의가 필요합니다.")
		case errors.Is(e, auth.ErrInvalid):
			failure(w, 400, "invalid_input", "이메일·비밀번호·닉네임과 필수 약관을 확인해 주세요.")
		case errors.Is(e, auth.ErrCredentials):
			w.Header().Set("WWW-Authenticate", "Bearer")
			failure(w, 401, "unauthorized", "인증 정보가 올바르지 않거나 만료되었습니다.")
		case errors.Is(e, auth.ErrEmailTaken):
			failure(w, 409, "email_conflict", "이미 사용 중인 이메일입니다.")
		case errors.Is(e, auth.ErrNicknameTaken):
			failure(w, 409, "nickname_conflict", "이미 사용 중인 닉네임입니다.")
		default:
			failure(w, 500, "internal_error", "요청을 처리하지 못했습니다.")
		}
	}
	ip := func(r *http.Request) string {
		host, _, e := net.SplitHostPort(r.RemoteAddr)
		if e != nil {
			return r.RemoteAddr
		}
		return host
	}
	limited := func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "300")
		failure(w, 429, "rate_limited", "잠시 후 다시 시도해 주세요.")
	}
	mux.HandleFunc("POST /api/v1/auth/signup", wrap(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow("signup:"+ip(r), 10) {
			limited(w)
			return
		}
		var in auth.Signup
		if !decode(w, r, &in) {
			return
		}
		u, e := s.Signup(r.Context(), in)
		result(w, 201, map[string]any{"user": u}, e)
	}))
	mux.HandleFunc("POST /api/v1/auth/login", wrap(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow("login-ip:"+ip(r), 20) {
			limited(w)
			return
		}
		var in auth.Login
		if !decode(w, r, &in) {
			return
		}
		email, e := auth.NormalizeEmail(in.Email)
		if e == nil && !limiter.allow("login-account:"+email, 5) {
			limited(w)
			return
		}
		v, e := s.Login(r.Context(), in)
		result(w, 200, v, e)
	}))
	mux.HandleFunc("POST /api/v1/auth/kakao", wrap(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow("kakao-login:"+ip(r), 20) {
			limited(w)
			return
		}
		var in auth.KakaoLogin
		if !decode(w, r, &in) {
			return
		}
		session, e := s.KakaoLogin(r.Context(), in)
		result(w, 200, session, e)
	}))
	mux.HandleFunc("POST /api/v1/auth/refresh", wrap(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow("refresh:"+ip(r), 60) {
			limited(w)
			return
		}
		var in auth.Refresh
		if !decode(w, r, &in) {
			return
		}
		v, e := s.Refresh(r.Context(), in.RefreshToken)
		result(w, 200, v, e)
	}))
	mux.HandleFunc("POST /api/v1/auth/logout", wrap(func(w http.ResponseWriter, r *http.Request) {
		var in auth.Refresh
		if !decode(w, r, &in) {
			return
		}
		if e := s.Logout(r.Context(), in.RefreshToken); e != nil {
			result(w, 0, nil, e)
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("GET /api/v1/users/me", wrap(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			result(w, 0, nil, auth.ErrCredentials)
			return
		}
		u, e := s.Me(r.Context(), parts[1])
		result(w, 200, map[string]any{"user": u}, e)
	}))
	mux.Handle("/", base)
	return mux
}
