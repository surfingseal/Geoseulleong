package kakao

import (
	"bytes"
	"context"
	"errors"
	"geoseulleong/internal/odsay"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxBody = 2 << 20

type ClientConfig struct {
	Approved    bool
	MaxRequests int
}
type Client struct {
	key        string
	http       *http.Client
	mu         sync.Mutex
	calls, max int
	last       time.Time
	interval   time.Duration
	capture    func(Capture) error
}

func (*Client) String() string     { return "Kakao routing client (credentials omitted)" }
func (c *Client) GoString() string { return c.String() }
func NewClient(key string, cfg ClientConfig) (*Client, error) {
	if !cfg.Approved {
		return nil, odsay.ErrDisabled
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "\r\n") || cfg.MaxRequests < 1 || cfg.MaxRequests > 10000 {
		return nil, odsay.ErrInput
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true, MaxConnsPerHost: 1, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second}
	return &Client{key: key, max: cfg.MaxRequests, interval: 2 * time.Second, http: &http.Client{Timeout: 8 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Calls() int { c.mu.Lock(); defer c.mu.Unlock(); return c.calls }
func (c *Client) Search(ctx context.Context, q odsay.Query) (odsay.Response, string, error) {
	if !q.Valid() {
		return odsay.Response{}, "kakao_live", odsay.ErrInput
	}
	endpoint := TransitEndpoint
	if q.Mode == "walk" {
		endpoint = WalkingEndpoint
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return odsay.Response{}, "kakao_live", ctx.Err()
	}
	if c.calls >= c.max {
		return odsay.Response{}, "kakao_live", odsay.ErrBudget
	}
	if delay := time.Until(c.last.Add(c.interval)); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return odsay.Response{}, "kakao_live", ctx.Err()
		case <-timer.C:
		}
	}
	values := url.Values{"start_x": {strconv.FormatFloat(q.Start.Lon, 'f', -1, 64)}, "start_y": {strconv.FormatFloat(q.Start.Lat, 'f', -1, 64)}, "end_x": {strconv.FormatFloat(q.End.Lon, 'f', -1, 64)}, "end_y": {strconv.FormatFloat(q.End.Lat, 'f', -1, 64)}, "input_coord": {"WGS84"}, "output_coord": {"WGS84"}}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+values.Encode(), nil)
	if e != nil {
		return odsay.Response{}, "kakao_live", odsay.ErrInput
	}
	req.Header.Set("Authorization", "KakaoAK "+c.key)
	req.Header.Set("Accept", "application/json")
	c.calls++
	c.last = time.Now()
	started := c.last
	res, e := c.http.Do(req)
	if e != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return odsay.Response{}, "kakao_live", odsay.ErrTimeout
		}
		return odsay.Response{}, "kakao_live", odsay.ErrProvider
	}
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	received := time.Now()
	if e != nil || len(raw) > maxBody || bytes.Contains(raw, []byte(c.key)) || bytes.Contains(raw, []byte(url.QueryEscape(c.key))) {
		return odsay.Response{}, "kakao_live", odsay.ErrProvider
	}
	if c.capture != nil {
		if e := c.capture(Capture{Request: Request{Start: q.Start, End: q.End}, StartedAt: started, ReceivedAt: received, HTTPStatus: res.StatusCode, Body: append([]byte(nil), raw...)}); e != nil {
			return odsay.Response{}, "kakao_live", odsay.ErrState
		}
	}
	if res.StatusCode == 429 {
		return odsay.Response{}, "kakao_live", odsay.ErrBudget
	}
	if res.StatusCode != http.StatusOK {
		return odsay.Response{}, "kakao_live", odsay.ErrProvider
	}
	var response odsay.Response
	if q.Mode == "walk" {
		response, e = ParseWalking(raw)
	} else {
		response, e = ParseTransit(raw)
	}
	return response, "kakao_live", e
}
func (c *Client) Walk(ctx context.Context, r WalkingRequest) (odsay.Response, string, error) {
	return c.Search(ctx, odsay.Query{Start: r.Start, End: r.End, Mode: "walk"})
}

type Provider struct {
	guard *odsay.Live
	wire  *Client
}

func NewLive(key string, cfg odsay.LiveConfig) (*Provider, error) {
	return NewLiveWithCapture(key, cfg, nil)
}

// NewLiveWithCapture preserves a bounded, credential-checked response before parsing.
// The sink receives no headers or authenticated URL and must not log its contents.
func NewLiveWithCapture(key string, cfg odsay.LiveConfig, sink func(Capture) error) (*Provider, error) {
	wire, e := NewClient(key, ClientConfig{Approved: cfg.Approved, MaxRequests: cfg.TotalLimit})
	if e != nil {
		return nil, e
	}
	wire.capture = sink
	guard, e := odsay.NewBudgetedProvider(wire, cfg)
	if e != nil {
		return nil, e
	}
	return &Provider{guard: guard, wire: wire}, nil
}
func (p *Provider) Search(ctx context.Context, q odsay.Query) (odsay.Response, string, error) {
	return p.guard.Search(ctx, q)
}
func (p *Provider) Walk(ctx context.Context, r WalkingRequest) (odsay.Response, string, error) {
	return p.Search(ctx, odsay.Query{Start: r.Start, End: r.End, Mode: "walk"})
}
func (p *Provider) Close() error { return p.guard.Close() }

// Calls counts HTTP attempts in this process, including failed transmissions.
func (p *Provider) Calls() int {
	if p.wire == nil {
		return 0
	}
	return p.wire.Calls()
}
