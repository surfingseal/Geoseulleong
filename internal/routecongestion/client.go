// Package routecongestion provides bounded collection of one explicitly approved query.
package routecongestion

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const Endpoint = "https://apis.data.go.kr/1613000/RouteCongestionLevel/getRouteCongestionLevel"

var ErrNoData = errors.New("API NO_DATA_FOUND")
var ErrResponse = errors.New("invalid or unsuccessful API response")

type Scope struct {
	Date     string `json:"opr_ymd"`
	Province string `json:"ctpv_cd"`
	District string `json:"sgg_cd"`
	Route    string `json:"rte_id"`
}

func ApprovedScope() Scope { return Scope{"20261105", "41", "41117", "41002905"} }

type Page struct {
	Code   string
	Total  int
	Number int
	Size   int
	Rows   []json.RawMessage
}

func field(m map[string]json.RawMessage, name string) json.RawMessage {
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return nil
}
func text(b json.RawMessage) string {
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	return string(b)
}
func integer(b json.RawMessage) (int, error) { return strconv.Atoi(text(b)) }

// Parse supports documented and observed Response/header/body/items envelopes.
func Parse(body []byte) (Page, error) {
	p := Page{Total: -1}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return p, ErrResponse
	}
	if b := field(root, "Error"); b != nil {
		var e map[string]json.RawMessage
		if json.Unmarshal(b, &e) != nil {
			return p, ErrResponse
		}
		p.Code = text(field(e, "code"))
		if p.Code == "50" {
			return p, ErrNoData
		}
		return p, ErrResponse
	}
	var response, header, data map[string]json.RawMessage
	if json.Unmarshal(field(root, "response"), &response) != nil || json.Unmarshal(field(response, "header"), &header) != nil {
		return p, ErrResponse
	}
	p.Code = text(field(header, "resultCode"))
	if p.Code != "0" && p.Code != "00" && p.Code != "200" {
		return p, ErrResponse
	}
	if json.Unmarshal(field(response, "body"), &data) != nil {
		return p, ErrResponse
	}
	var e error
	p.Total, e = integer(field(data, "totalCount"))
	if e != nil || p.Total < 0 {
		return p, ErrResponse
	}
	p.Number, e = integer(field(data, "pageNo"))
	if e != nil || p.Number < 1 {
		return p, ErrResponse
	}
	p.Size, e = integer(field(data, "numOfRows"))
	if e != nil || p.Size < 1 {
		return p, ErrResponse
	}
	b := field(data, "items")
	if len(b) == 0 || string(b) == "null" || string(b) == `""` {
		if p.Total == 0 {
			p.Rows = []json.RawMessage{}
			return p, nil
		}
		return p, ErrResponse
	}
	var items map[string]json.RawMessage
	if json.Unmarshal(b, &items) != nil {
		return p, ErrResponse
	}
	b = field(items, "item")
	if len(b) == 0 || string(b) == "null" {
		if p.Total == 0 {
			p.Rows = []json.RawMessage{}
			return p, nil
		}
		return p, ErrResponse
	}
	if json.Unmarshal(b, &p.Rows) != nil {
		var one map[string]json.RawMessage
		if json.Unmarshal(b, &one) != nil {
			return p, ErrResponse
		}
		p.Rows = []json.RawMessage{b}
	}
	return p, nil
}

// MaxReadBytes includes the byte used to detect an oversized response.
const MaxBodyBytes = 16 << 20
const MaxReadBytes = MaxBodyBytes + 1

// TransferReceipt contains counts only, never request URLs, headers or body text.
// BodyComplete means EOF was observed within the accepted body limit.
// A partial/unknown transfer must retain its full reservation.
type TransferReceipt struct {
	BodyBytes        int64 `json:"body_bytes_read"`
	BytesKnown       bool  `json:"body_counter_available"`
	BodyComplete     bool  `json:"body_complete"`
	WriteAttempts    int64 `json:"http_write_attempts"`
	ConnectionReused bool  `json:"connection_reused"`
}

// CollectionTransport uses a fresh HTTP/1.1 connection for each request. Go's
// reused-connection retries and HTTP/2 retry paths are therefore unavailable.
func CollectionTransport() *http.Transport {
	return &http.Transport{
		Proxy:             nil, // Direct connections avoid an unbudgeted HTTP CONNECT/proxy replay layer.
		DialContext:       (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: -1}).DialContext,
		DisableKeepAlives: true, DisableCompression: true, ForceAttemptHTTP2: false,
		TLSNextProto:    map[string]func(string, *tls.Conn) http.RoundTripper{},
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}},
		MaxConnsPerHost: 1, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
		MaxResponseHeaderBytes: 1 << 20,
	}
}

type Client struct {
	mu            sync.Mutex
	endpoint, key string
	http          *http.Client
	interval      time.Duration
	last          time.Time
	Calls         int
	approved      map[Scope]bool
	localMock     bool
}

func New(key string) (*Client, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("API key is not configured")
	}
	if strings.Contains(key, "%") {
		var e error
		key, e = url.PathUnescape(key)
		if e != nil {
			return nil, errors.New("invalid API key encoding")
		}
	}
	return &Client{endpoint: Endpoint, key: key, http: &http.Client{Transport: CollectionTransport(), Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, interval: 2 * time.Second}, nil
}

// Get preserves the legacy Fetcher contract. New runners use GetMeasured so
// discarded/partial bodies are charged even when raw must not be returned.
func (c *Client) Get(ctx context.Context, scope Scope, page int) ([]byte, int, error) {
	raw, status, _, err := c.GetMeasured(ctx, scope, page)
	return raw, status, err
}
func (c *Client) GetMeasured(ctx context.Context, scope Scope, page int) (raw []byte, status int, receipt TransferReceipt, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	allowed := scope.Allowed()
	if c.approved != nil {
		allowed = c.approved[scope]
	}
	if !allowed || page < 1 {
		return nil, 0, receipt, errors.New("query scope not authorized")
	}
	if e := ctx.Err(); e != nil {
		return nil, 0, receipt, errors.New("request canceled")
	}
	if delay := time.Until(c.last.Add(c.interval)); delay > 0 {
		t := time.NewTimer(delay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return nil, 0, receipt, errors.New("request canceled")
		}
	}
	q := url.Values{"serviceKey": {c.key}, "opr_ymd": {scope.Date}, "ctpv_cd": {scope.Province}, "sgg_cd": {scope.District}, "rte_id": {scope.Route}, "pageNo": {strconv.Itoa(page)}, "numOfRows": {"1000"}, "dataType": {"JSON"}}
	req, e := http.NewRequestWithContext(ctx, "GET", c.endpoint+"?"+q.Encode(), nil)
	if e != nil {
		return nil, 0, receipt, errors.New("request configuration failed")
	}
	// DisableKeepAlives plus HTTP/1.1 plus Close prevents a request from ever
	// reaching a reused persistConn, including a server that advertises keep-alive.
	req.Close = true
	req.Header.Set("Accept", "application/json")
	var writes atomic.Int64
	var reused atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { writes.Add(1) }, GotConn: func(i httptrace.GotConnInfo) {
		if i.Reused {
			reused.Store(true)
		}
	}}
	req = req.WithContext(httptrace.WithClientTrace(ctx, trace))
	defer func() {
		receipt.WriteAttempts = writes.Load()
		receipt.ConnectionReused = reused.Load()
		c.last = time.Now()
	}()
	c.Calls++
	res, e := c.http.Do(req)
	if e != nil {
		return nil, 0, receipt, errors.New("transport or timeout failure")
	}
	defer res.Body.Close()
	body, e := io.ReadAll(io.LimitReader(res.Body, MaxReadBytes))
	receipt.BodyBytes = int64(len(body))
	receipt.BytesKnown = true
	receipt.BodyComplete = e == nil && len(body) <= MaxBodyBytes
	if e != nil {
		return nil, res.StatusCode, receipt, errors.New("body read failure")
	}
	if len(body) > MaxBodyBytes {
		return nil, res.StatusCode, receipt, errors.New("response too large")
	}
	for _, secret := range []string{c.key, url.QueryEscape(c.key), url.PathEscape(c.key)} {
		if strings.Contains(string(body), secret) {
			return nil, res.StatusCode, receipt, errors.New("credential echoed in response; not saved")
		}
	}
	if res.StatusCode != http.StatusOK {
		return body, res.StatusCode, receipt, errors.New("HTTP response failure")
	}
	return body, res.StatusCode, receipt, nil
}

// NewLocalTestClient exercises the real client on an explicit loopback HTTP
// mock only; production uses New/NewScoped and cannot redirect to remote URLs.
func NewLocalTestClient(endpoint, key string, scopes []Scope) (*Client, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("loopback mock URL required")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("loopback mock URL required")
	}
	c, e := NewScoped(key, scopes)
	if e != nil {
		return nil, e
	}
	c.endpoint = endpoint
	c.localMock = true
	c.http.Transport.(*http.Transport).Proxy = nil
	return c, nil
}

// Allowed restricts requests to explicitly approved dates, route and province/district pairs.
func (s Scope) Allowed() bool {
	if s.Route != "41002905" {
		return false
	}
	if s.Date == "20261105" {
		return s.Province == "41" && s.District == "41117"
	}
	if s.Date != "20261001" {
		return false
	}
	switch s.Province {
	case "11":
		switch s.District {
		case "11110", "11140", "11170", "11650", "11680":
			return true
		}
	case "41":
		switch s.District {
		case "41117", "41131", "41135", "41463", "41465":
			return true
		}
	}
	return false
}

// NewScoped requires a separately verified exact manifest gate; New retains its legacy allowlist.
func NewScoped(key string, scopes []Scope) (*Client, error) {
	if len(scopes) == 0 {
		return nil, errors.New("explicit verified scopes required")
	}
	c, e := New(key)
	if e != nil {
		return nil, e
	}
	c.approved = map[Scope]bool{}
	for _, s := range scopes {
		c.approved[s] = true
	}
	return c, nil
}

// DataOrigin describes the transport, not whether a remote dataset exists.
func (c *Client) DataOrigin() string {
	if c.localMock {
		return "local_mock_http"
	}
	return "molit_live_response"
}
