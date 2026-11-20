package odsay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const Endpoint = "https://api.odsay.com/v1/api/searchPubTransPathT"

type Config struct {
	Key         string
	Approved    bool
	MaxRequests int
	// Capture receives a credential-checked response once, before schema parsing.
	Capture func(int, []byte, time.Time) error
}
type Client struct {
	key, endpoint string
	http          *http.Client
	mu            sync.Mutex
	calls, max    int
	last          time.Time
	interval      time.Duration
	capture       func(int, []byte, time.Time) error
}

func NewClient(cfg Config) (*Client, error) {
	if !cfg.Approved {
		return nil, ErrDisabled
	}
	if strings.TrimSpace(cfg.Key) == "" || cfg.MaxRequests != 1 {
		return nil, errors.New("ODsay key and explicit single-request budget required")
	}
	return &Client{key: cfg.Key, endpoint: Endpoint, http: &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true, MaxConnsPerHost: 1, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, max: cfg.MaxRequests, capture: cfg.Capture, interval: 2 * time.Second}, nil
}
func (c *Client) Search(ctx context.Context, q Query) (Response, string, error) {
	if q.Mode == "walk" {
		return Response{}, "odsay_live", ErrUnsupportedMode
	}
	if !q.Valid() {
		return Response{}, "odsay_live", ErrInput
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return Response{}, "odsay_live", ctx.Err()
	}
	if c.calls >= c.max {
		return Response{}, "odsay_live", ErrBudget
	}
	if wait := time.Until(c.last.Add(c.interval)); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return Response{}, "odsay_live", errors.New("ODsay request canceled")
		}
	}
	mode := "0"
	if q.Mode == "bus" {
		mode = "2"
	}
	if q.Mode == "subway" {
		mode = "1"
	}
	values := url.Values{"apiKey": {c.key}, "SX": {strconv.FormatFloat(q.Start.Lon, 'f', -1, 64)}, "SY": {strconv.FormatFloat(q.Start.Lat, 'f', -1, 64)}, "EX": {strconv.FormatFloat(q.End.Lon, 'f', -1, 64)}, "EY": {strconv.FormatFloat(q.End.Lat, 'f', -1, 64)}, "OPT": {"0"}, "SearchType": {"0"}, "SearchPathType": {mode}, "lang": {"0"}, "output": {"json"}}
	req, e := http.NewRequestWithContext(ctx, "GET", c.endpoint+"?"+values.Encode(), nil)
	if e != nil {
		return Response{}, "odsay_live", errors.Join(ErrProvider, errors.New("ODsay request configuration failed"))
	}
	c.calls++
	c.last = time.Now()
	res, e := c.http.Do(req)
	if e != nil {
		var timeout net.Error
		if errors.As(e, &timeout) && timeout.Timeout() {
			return Response{}, "odsay_live", ErrTimeout
		}
		return Response{}, "odsay_live", errors.Join(ErrProvider, errors.New("ODsay transport failure; transmission may have occurred; no retry"))
	}
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if e != nil || len(raw) > 4<<20 {
		return Response{}, "odsay_live", ErrSchema
	}
	if echoesCredential(raw, c.key) {
		return Response{}, "odsay_live", errors.Join(ErrProvider, errors.New("credential echoed; response discarded"))
	}

	if c.capture != nil {
		if err := c.capture(res.StatusCode, raw, time.Now().UTC()); err != nil {
			return Response{}, "odsay_live", errors.Join(ErrProvider, errors.New("ODsay response archive failed; no retry"))
		}
	}
	if res.StatusCode != 200 {
		return Response{}, "odsay_live", ErrProvider
	}
	r, e := Parse(raw)
	return r, "odsay_live", e
}

// Fixture uses explicit synthetic provider data, with input-coordinate binding.
type Fixture struct {
	Raw        []byte
	Start, End Point
}

func (f Fixture) Search(ctx context.Context, q Query) (Response, string, error) {
	if q.Start != f.Start || q.End != f.End {
		return Response{}, "synthetic_fixture", errors.New("coordinates do not match the synthetic fixture")
	}
	r, e := Parse(f.Raw)
	return r, "synthetic_fixture", e
}

// Disabled is wired into the ordinary server until a separately approved live client is supplied.
type Disabled struct{}

func (Disabled) Search(context.Context, Query) (Response, string, error) {
	return Response{}, "disabled", ErrDisabled
}

// JSON string escapes must not bypass credential filtering before archiving or returning data.
func echoesCredential(raw []byte, key string) bool {
	secrets := []string{key, url.QueryEscape(key)}
	contains := func(s string) bool {
		for _, secret := range secrets {
			if strings.Contains(s, secret) {
				return true
			}
		}
		return false
	}
	if contains(string(raw)) {
		return true
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case string:
			return contains(x)
		case []any:
			for _, v := range x {
				if walk(v) {
					return true
				}
			}
		case map[string]any:
			for k, v := range x {
				if contains(k) || walk(v) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
}
