package gbis

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ValidationCapture struct {
	APICode   string    `json:"api_code,omitempty"`
	Operation string    `json:"operation"`
	Route     string    `json:"route_id"`
	Station   string    `json:"station_id,omitempty"`
	Order     string    `json:"station_order,omitempty"`
	Started   time.Time `json:"request_started_at"`
	Received  time.Time `json:"received_at"`
	HTTP      int       `json:"http_status"`
	Body      []byte    `json:"-"`
}
type validationProfile struct {
	realtime    bool
	before      func(context.Context) error
	arrivals    int
	location    bool
	maxArrivals int
	seen        map[string]bool
	sink        func(ValidationCapture) error
}

// NewValidationClient is a separately approved, operation-limited diagnostic
// profile. General NewLimited limits remain unchanged. No automatic retry.
// Live CLI use must go through NewPersistentValidationClient.
func NewValidationClient(key string, approved bool, maxArrivals int, sink func(ValidationCapture) error) (*Client, error) {
	if !approved {
		return nil, &Error{Kind: "live_validation_disabled"}
	}
	if maxArrivals < 1 || maxArrivals > 4 {
		return nil, &Error{Kind: "invalid_validation_limit"}
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true, MaxConnsPerHost: 1, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	c, e := newClient(BaseURL, key, &http.Client{Transport: transport, Timeout: 15 * time.Second}, 2*time.Second, 1+maxArrivals)
	if e != nil {
		return nil, e
	}
	c.validation = &validationProfile{maxArrivals: maxArrivals, seen: map[string]bool{}, sink: sink}
	return c, nil
}

// allow is called while the existing client mutex is held. A rejected/reserved
// attempt is never reopened on failure; explicit callers cannot retry a stop.
func (p *validationProfile) allow(op string, q url.Values) error {
	if p.realtime {
		switch op {
		case "/buslocationservice/v2/getBusLocationListv2", "/busarrivalservice/v2/getBusArrivalItemv2":
			return nil
		default:
			return &Error{Kind: "operation_not_allowed"}
		}
	}
	switch op {
	case "/buslocationservice/v2/getBusLocationListv2":
		if p.location {
			return &Error{Kind: "operation_limit"}
		}
		p.location = true
	case "/busarrivalservice/v2/getBusArrivalItemv2":
		key := q.Get("routeId") + "/" + q.Get("stationId") + "/" + q.Get("staOrder")
		if p.arrivals >= p.maxArrivals || p.seen[key] {
			return &Error{Kind: "operation_limit"}
		}
		p.arrivals++
		p.seen[key] = true
	default:
		return &Error{Kind: "operation_not_allowed"}
	}
	return nil
}
func (p *validationProfile) captureBody(key, op string, q url.Values, start, received time.Time, status int, raw []byte) error {
	if reflected(raw, key) {
		return &Error{Kind: "credential_reflection_blocked", HTTPStatus: status}
	}
	decoded := []byte(html.UnescapeString(string(raw)))
	if bytes.Contains(decoded, []byte(key)) || bytes.Contains(raw, []byte(url.QueryEscape(key))) || bytes.Contains(raw, []byte(url.PathEscape(key))) {
		return &Error{Kind: "credential_reflection_blocked", HTTPStatus: status}
	}
	apiCode := ""
	var root node
	if xml.Unmarshal(raw, &root) == nil {
		if h, ok := root.child("msgHeader"); ok {
			apiCode = code(h.val("resultCode"))
		}
	}
	if p.sink != nil {
		if e := p.sink(ValidationCapture{APICode: apiCode, Operation: op, Route: q.Get("routeId"), Station: q.Get("stationId"), Order: q.Get("staOrder"), Started: start, Received: received, HTTP: status, Body: append([]byte(nil), raw...)}); e != nil {
			return &Error{Kind: "capture_failed", HTTPStatus: status}
		}
	}
	return nil
}

func reflected(raw []byte, key string) bool {
	contains := func(s string) bool {
		if strings.Contains(html.UnescapeString(s), key) {
			return true
		}
		if decoded, e := url.QueryUnescape(s); e == nil && strings.Contains(html.UnescapeString(decoded), key) {
			return true
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
	var scan func(any) bool
	scan = func(v any) bool {
		switch x := v.(type) {
		case string:
			return contains(x)
		case []any:
			for _, v := range x {
				if scan(v) {
					return true
				}
			}
		case map[string]any:
			for k, v := range x {
				if contains(k) || scan(v) {
					return true
				}
			}
		}
		return false
	}
	return scan(v)
}
