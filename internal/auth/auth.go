// Package auth owns validation and token policy. It never logs credentials.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var (
	ErrInvalid       = errors.New("invalid input")
	ErrCredentials   = errors.New("invalid credentials or token")
	ErrEmailTaken    = errors.New("email already registered")
	ErrNicknameTaken = errors.New("nickname already registered")
)

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Nickname  string    `json:"nickname"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}
type Account struct {
	User
	PasswordHash string `json:"-"`
}
type Signup struct {
	Email           string `json:"email"`
	Password        string `json:"password"`
	PasswordConfirm string `json:"password_confirm"`
	Nickname        string `json:"nickname"`
	TermsAgreed     bool   `json:"terms_agreed"`
	PrivacyAgreed   bool   `json:"privacy_agreed"`
}
type Login struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}
type Refresh struct {
	RefreshToken string `json:"refresh_token"`
}
type Token struct {
	ID, FamilyID, UserID string
	Hash                 []byte
	CreatedAt, ExpiresAt time.Time
}
type Session struct {
	User             User   `json:"user"`
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
}
type Store interface {
	Create(context.Context, Account, string, string, string, time.Time) error
	Find(context.Context, string) (Account, error)
	NewSession(context.Context, Token) error
	Rotate(context.Context, []byte, Token, time.Time) (User, time.Time, error)
	Logout(context.Context, []byte, time.Time) error
	Current(context.Context, string, string, time.Time) (User, error)
}
type Config struct {
	Kakao                        KakaoVerifier
	Secret                       []byte
	TermsVersion, PrivacyVersion string
	Cost                         int
	Now                          func() time.Time
}
type Service struct {
	store Store
	cfg   Config
	dummy []byte
}

func New(store Store, c Config) (*Service, error) {
	if store == nil || len(c.Secret) < 32 || strings.TrimSpace(c.TermsVersion) == "" || len(c.TermsVersion) > 64 || strings.TrimSpace(c.PrivacyVersion) == "" || len(c.PrivacyVersion) > 64 {
		return nil, errors.New("auth configuration invalid")
	}
	if c.Cost == 0 {
		c.Cost = 12
	}
	if c.Cost < 4 || c.Cost > 14 {
		return nil, errors.New("auth bcrypt cost invalid")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	c.Secret = append([]byte(nil), c.Secret...)
	dummy, e := bcrypt.GenerateFromPassword([]byte("dummy-password-never-used"), c.Cost)
	if e != nil {
		return nil, e
	}
	return &Service{store: store, cfg: c, dummy: dummy}, nil
}
func NormalizeEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 254 || len(s) < 3 {
		return "", ErrInvalid
	}
	for _, r := range s {
		if r > 127 || unicode.IsControl(r) {
			return "", ErrInvalid
		}
	}
	a, e := mail.ParseAddress(s)
	if e != nil || a.Address != s || !strings.Contains(strings.SplitN(s, "@", 2)[1], ".") {
		return "", ErrInvalid
	}
	return s, nil
}
func ValidPassword(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) < 8 || utf8.RuneCountInString(s) > 20 || len(s) > 72 {
		return false
	}
	var letter, digit, special bool
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			letter = true
		}
		if r >= '0' && r <= '9' {
			digit = true
		}
		if r >= 33 && r <= 126 && !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			special = true
		}
	}
	return letter && digit && special
}
func Nickname(s string) (string, string, error) {
	if !utf8.ValidString(s) {
		return "", "", ErrInvalid
	}
	s = norm.NFC.String(s)
	if utf8.RuneCountInString(s) < 2 || utf8.RuneCountInString(s) > 20 {
		return "", "", ErrInvalid
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", "", ErrInvalid
		}
	}
	return s, norm.NFC.String(cases.Fold().String(s)), nil
}
func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic("secure random unavailable")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func Hash(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
func validRefresh(s string) bool {
	b, e := base64.RawURLEncoding.DecodeString(s)
	return e == nil && len(b) == 32 && len(s) == 43
}
func newToken(uid string, now time.Time) (Token, string) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic("secure random unavailable")
	}
	raw := base64.RawURLEncoding.EncodeToString(b)
	id := ID()
	return Token{ID: id, FamilyID: id, UserID: uid, Hash: Hash(raw), CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour)}, raw
}
func (s *Service) Signup(ctx context.Context, in Signup) (User, error) {
	email, e := NormalizeEmail(in.Email)
	if e != nil || !ValidPassword(in.Password) || in.Password != in.PasswordConfirm || !in.TermsAgreed || !in.PrivacyAgreed {
		return User{}, ErrInvalid
	}
	nick, key, e := Nickname(in.Nickname)
	if e != nil {
		return User{}, e
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(in.Password), s.cfg.Cost)
	if e != nil {
		return User{}, e
	}
	now := s.cfg.Now().UTC()
	a := Account{User: User{ID: ID(), Email: email, Nickname: nick, Status: "active", CreatedAt: now}, PasswordHash: string(hash)}
	e = s.store.Create(ctx, a, key, s.cfg.TermsVersion, s.cfg.PrivacyVersion, now)
	return a.User, e
}
func (s *Service) Login(ctx context.Context, in Login) (Session, error) {
	email, e := NormalizeEmail(in.Email)
	if e != nil || len(in.Password) > 72 || len(in.Password) == 0 {
		return Session{}, ErrCredentials
	}
	a, e := s.store.Find(ctx, email)
	hash := s.dummy
	if e == nil {
		hash = []byte(a.PasswordHash)
	}
	ok := bcrypt.CompareHashAndPassword(hash, []byte(in.Password)) == nil
	if e != nil {
		if !errors.Is(e, ErrCredentials) {
			return Session{}, e
		}
		return Session{}, ErrCredentials
	}
	if !ok || a.Status != "active" {
		return Session{}, ErrCredentials
	}
	t, raw := newToken(a.ID, s.cfg.Now().UTC())
	if e = s.store.NewSession(ctx, t); e != nil {
		return Session{}, e
	}
	return s.session(a.User, t, raw)
}
func (s *Service) Refresh(ctx context.Context, raw string) (Session, error) {
	if !validRefresh(raw) {
		return Session{}, ErrCredentials
	}
	now := s.cfg.Now().UTC()
	t, next := newToken("", now)
	u, expiry, e := s.store.Rotate(ctx, Hash(raw), t, now)
	if e != nil {
		return Session{}, e
	}
	t.UserID = u.ID
	t.ExpiresAt = expiry
	return s.session(u, t, next)
}
func (s *Service) Logout(ctx context.Context, raw string) error {
	if !validRefresh(raw) {
		return ErrCredentials
	}
	return s.store.Logout(ctx, Hash(raw), s.cfg.Now().UTC())
}

type claims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

func (s *Service) session(u User, t Token, raw string) (Session, error) {
	now := s.cfg.Now().UTC()
	c := claims{SessionID: t.ID, RegisteredClaims: jwt.RegisteredClaims{Issuer: "geoseulleong", Subject: u.ID, Audience: jwt.ClaimStrings{"geoseulleong-api"}, ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)), IssuedAt: jwt.NewNumericDate(now), ID: ID()}}
	signed, e := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(s.cfg.Secret)
	return Session{User: u, AccessToken: signed, RefreshToken: raw, TokenType: "Bearer", ExpiresIn: 900, RefreshExpiresIn: int64(t.ExpiresAt.Sub(now).Seconds())}, e
}
func (s *Service) Me(ctx context.Context, raw string) (User, error) {
	if len(raw) > 4096 {
		return User{}, ErrCredentials
	}
	c := &claims{}
	tok, e := jwt.ParseWithClaims(raw, c, func(*jwt.Token) (any, error) { return s.cfg.Secret, nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("geoseulleong"), jwt.WithAudience("geoseulleong-api"), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithTimeFunc(s.cfg.Now))
	if e != nil || !tok.Valid || c.Subject == "" || c.SessionID == "" {
		return User{}, ErrCredentials
	}
	return s.store.Current(ctx, c.Subject, c.SessionID, s.cfg.Now().UTC())
}
