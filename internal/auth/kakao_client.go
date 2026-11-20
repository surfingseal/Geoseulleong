package auth

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Kakao verifies the SDK token using the two official Bearer-token endpoints.
// No REST/native/admin key and no user token is retained by the client.
type KakaoClient struct {
	appID   int64
	http    *http.Client
	baseURL string
}

func (c *KakaoClient) String() string   { return "Kakao login verifier (no tokens retained)" }
func (c *KakaoClient) GoString() string { return c.String() }
func NewKakaoClient(appID int64) (*KakaoClient, error) {
	if appID <= 0 {
		return nil, ErrInvalid
	}
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true, MaxConnsPerHost: 1, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext, TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 3 * time.Second}
	return &KakaoClient{appID: appID, baseURL: "https://kapi.kakao.com", http: &http.Client{Timeout: 3 * time.Second, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *KakaoClient) get(ctx context.Context, path, token string, out any) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if e != nil {
		return ErrKakaoUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	req.Header.Set("Accept", "application/json")
	resp, e := c.http.Do(req)
	if e != nil {
		return ErrKakaoUnavailable
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 128*1024+1))
	if e != nil || len(raw) > 128*1024 {
		return ErrKakaoUnavailable
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrCredentials
	}
	if resp.StatusCode == http.StatusBadRequest {
		var v struct {
			Code int `json:"code"`
		}
		if json.Unmarshal(raw, &v) == nil && v.Code == -1 {
			return ErrKakaoUnavailable
		}
		return ErrCredentials
	}
	if resp.StatusCode != http.StatusOK {
		return ErrKakaoUnavailable
	}
	if json.Unmarshal(raw, out) != nil {
		return ErrKakaoUnavailable
	}
	return nil
}
func (c *KakaoClient) Verify(ctx context.Context, token string) (KakaoIdentity, error) {
	if !validKakaoToken(token) {
		return KakaoIdentity{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var info struct {
		ID      int64  `json:"id"`
		AppID   int64  `json:"app_id"`
		Expires *int64 `json:"expires_in"`
	}
	if e := c.get(ctx, "/v1/user/access_token_info", token, &info); e != nil {
		return KakaoIdentity{}, e
	}
	if info.ID <= 0 || info.AppID != c.appID || info.Expires == nil || *info.Expires <= 0 {
		return KakaoIdentity{}, ErrCredentials
	}
	var user struct {
		ID       int64 `json:"id"`
		SignedUp *bool `json:"has_signed_up"`
	}
	// Request only the profile set; email and all other PII are neither requested nor stored.
	if e := c.get(ctx, "/v2/user/me?secure_resource=true&property_keys=%5B%22kakao_account.profile%22%5D", token, &user); e != nil {
		return KakaoIdentity{}, e
	}
	if user.ID != info.ID || user.SignedUp != nil && !*user.SignedUp {
		return KakaoIdentity{}, ErrCredentials
	}
	return KakaoIdentity{UserID: strconv.FormatInt(user.ID, 10)}, nil
}
