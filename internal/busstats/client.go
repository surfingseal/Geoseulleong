package busstats

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Confirmed in the supplied official workbook, Sheet1!A7, A22:A25.
const Endpoint = "https://openapi.gg.go.kr/TBGGMETROPLBUSTKALGHTPSN"
const Service = "TBGGMETROPLBUSTKALGHTPSN"
const PageSize = 100
const MaxResponseBytes = 4 << 20

type APIError struct {
	Kind       string `json:"kind"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	Code       string `json:"code,omitempty"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("승하차 통계 오류: %s (HTTP=%d, 코드=%s)", e.Kind, e.HTTPStatus, e.Code)
}

type Client struct {
	key, endpoint string
	http          *http.Client
	mu            sync.Mutex
	calls         int
}

func NewClient(key string) (*Client, error) {
	return newStatsClient(key, Endpoint, &http.Client{Timeout: 20 * time.Second})
}
func newStatsClient(key, endpoint string, h *http.Client) (*Client, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("GG_BUS_STATS_API_KEY 환경변수가 없습니다")
	}
	hc := *h
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{key: key, endpoint: endpoint, http: &hc}, nil
}
func (c *Client) Calls() int { c.mu.Lock(); defer c.mu.Unlock(); return c.calls }

// FetchFirstPage makes one attempt, never retries, and documents no guessed
// route/date filters. One Client permits at most one request.
func (c *Client) FetchFirstPage(ctx context.Context) ([]byte, int, error) {
	return c.FetchSamplePage(ctx, 1)
}

// FetchSamplePage requests one explicitly chosen page, never iterates pages.
// pIndex is a documented basic parameter; no search filters are inferred.
func (c *Client) FetchSamplePage(ctx context.Context, page int) ([]byte, int, error) {
	if page < 1 {
		return nil, 0, &APIError{Kind: "invalid_page"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls != 0 {
		return nil, 0, &APIError{Kind: "request_limit"}
	}
	q := url.Values{"Key": {c.key}, "Type": {"json"}, "pIndex": {strconv.Itoa(page)}, "pSize": {"100"}}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"?"+q.Encode(), nil)
	if e != nil {
		return nil, 0, &APIError{Kind: "request_configuration"}
	}
	c.calls++
	res, e := c.http.Do(req)
	if e != nil {
		return nil, 0, &APIError{Kind: "transport_or_timeout"}
	}
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, MaxResponseBytes+1))
	if e != nil {
		return nil, res.StatusCode, &APIError{Kind: "body_read", HTTPStatus: res.StatusCode}
	}
	if len(raw) > MaxResponseBytes {
		return nil, res.StatusCode, &APIError{Kind: "response_too_large", HTTPStatus: res.StatusCode}
	}
	// Preserve the response separately, but redact any echoed credential.
	for _, secret := range []string{c.key, url.QueryEscape(c.key), url.PathEscape(c.key)} {
		if secret != "" {
			raw = bytes.ReplaceAll(raw, []byte(secret), []byte("[REDACTED]"))
		}
	}
	if res.StatusCode != 200 {
		return raw, res.StatusCode, &APIError{Kind: "http", HTTPStatus: res.StatusCode}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw, 200, &APIError{Kind: "empty_response"}
	}
	return raw, res.StatusCode, nil
}
