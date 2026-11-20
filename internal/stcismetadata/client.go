// Package stcismetadata implements documented metadata APIs. No automatic or background calls.
package stcismetadata

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const BaseURL = "https://stcis.go.kr/openapi/"

var ErrResponse = errors.New("STCIS metadata response invalid or unsuccessful")

type Stop struct {
	ID       string `json:"sttnId"`
	BISID    string `json:"bimsId"`
	Name     string `json:"sttnNm"`
	ARS      string `json:"sttnArsno"`
	Province string `json:"sdCd"`
	District string `json:"sggCd"`
	Dong     string `json:"emdCd"`
}
type RouteStop struct {
	Route        string `json:"routeId"`
	RouteNumber  string `json:"routeNo"`
	ID           string `json:"sttnId"`
	Name         string `json:"sttnNm"`
	Sequence     int    `json:"sttnSeq"`
	Province     string `json:"sdCd"`
	District     string `json:"sggCd"`
	Dong         string `json:"emdCd"`
	RouteName    string `json:"routeNm"`
	ProvinceName string `json:"sdNm"`
	DistrictName string `json:"sggNm"`
	DongName     string `json:"emdNm"`
}

func scalar(r json.RawMessage) string {
	var s string
	if json.Unmarshal(r, &s) == nil {
		return s
	}
	if string(r) == "null" {
		return ""
	}
	return string(r)
}
func envelope(raw []byte) ([]json.RawMessage, string, error) {
	var r struct {
		Count  *int              `json:"count"`
		Status string            `json:"status"`
		Rows   []json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Count == nil || *r.Count < 0 {
		return nil, "", ErrResponse
	}
	if r.Status == "NOT_FOUND" && *r.Count == 0 && len(r.Rows) == 0 {
		return []json.RawMessage{}, r.Status, nil
	}
	if r.Status != "OK" || *r.Count != len(r.Rows) {
		return nil, r.Status, ErrResponse
	}
	return r.Rows, r.Status, nil
}
func ParseStops(raw []byte) ([]Stop, string, error) {
	rows, status, e := envelope(raw)
	if e != nil {
		return nil, status, e
	}
	out := []Stop{}
	for _, r := range rows {
		var s Stop
		if json.Unmarshal(r, &s) != nil || s.ID == "" || s.Name == "" {
			return nil, status, ErrResponse
		}
		out = append(out, s)
	}
	return out, status, nil
}
func ParseRouteStops(raw []byte, route, province string) ([]RouteStop, string, error) {
	rows, status, e := envelope(raw)
	if e != nil {
		return nil, status, e
	}
	out := []RouteStop{}
	for _, r := range rows {
		var m map[string]json.RawMessage
		if json.Unmarshal(r, &m) != nil {
			return nil, status, ErrResponse
		}
		seq, e := strconv.Atoi(scalar(m["sttnSeq"]))
		s := RouteStop{Route: scalar(m["routeId"]), RouteNumber: scalar(m["routeNo"]), ID: scalar(m["sttnId"]), Name: scalar(m["sttnNm"]), Sequence: seq, Province: scalar(m["sdCd"]), District: scalar(m["sggCd"]), Dong: scalar(m["emdCd"]), RouteName: scalar(m["routeNm"]), ProvinceName: scalar(m["sdNm"]), DistrictName: scalar(m["sggNm"]), DongName: scalar(m["emdNm"])}
		if e != nil || seq < 0 || s.Route != route || s.Province != province || s.ID == "" || s.Name == "" {
			return nil, status, ErrResponse
		}
		out = append(out, s)
	}
	return out, status, nil
}

type Client struct {
	key, base       string
	http            *http.Client
	last            time.Time
	interval        time.Duration
	Calls, MaxCalls int
}

func New(key string, max int) (*Client, error) {
	if strings.TrimSpace(key) == "" || max < 1 || max > 5 {
		return nil, errors.New("STCIS key or explicit metadata budget missing")
	}
	return &Client{key: key, base: BaseURL, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, interval: 2 * time.Second, MaxCalls: max}, nil
}
func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, int, error) {
	if c.Calls >= c.MaxCalls {
		return nil, 0, errors.New("STCIS metadata budget reached")
	}
	if wait := time.Until(c.last.Add(c.interval)); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, 0, errors.New("request canceled")
		}
	}
	q.Set("apikey", c.key)
	req, e := http.NewRequestWithContext(ctx, "GET", c.base+path+"?"+q.Encode(), nil)
	if e != nil {
		return nil, 0, errors.New("metadata request configuration failed")
	}
	c.Calls++
	c.last = time.Now()
	res, e := c.http.Do(req)
	if e != nil {
		return nil, 0, errors.New("metadata transport failure")
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if e != nil || len(b) > 8<<20 {
		return nil, res.StatusCode, errors.New("metadata response size/read failure")
	}
	for _, k := range []string{c.key, url.QueryEscape(c.key)} {
		if strings.Contains(string(b), k) {
			return nil, res.StatusCode, errors.New("credential echoed; response not retained")
		}
	}
	if res.StatusCode != 200 {
		return b, res.StatusCode, errors.New("metadata HTTP failure")
	}
	return b, res.StatusCode, nil
}
func (c *Client) RouteStops(ctx context.Context, province, route string) ([]byte, int, error) {
	if (province != "11" && province != "41") || route != "41002905" {
		return nil, 0, errors.New("unapproved metadata scope")
	}
	return c.get(ctx, "busroutesttn.json", url.Values{"sdCd": {province}, "routeId": {route}})
}
func (c *Client) Stops(ctx context.Context, province, district, dong string) ([]byte, int, error) {
	if !(province == "41" && district == "41117" || province == "11" && (district == "11140" || district == "11170")) || dong != "" && !(province == "11" && district == "11170" && dong == "1117013100") {
		return nil, 0, errors.New("unapproved metadata scope")
	}
	q := url.Values{"sdCd": {province}, "sggCd": {district}}
	if dong != "" {
		q.Set("emdCd", dong)
	}
	return c.get(ctx, "bussttn.json", q)
}
