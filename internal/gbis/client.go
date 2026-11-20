// Package gbis implements the officially documented GBIS v2 XML operations.
package gbis

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const BaseURL = "https://apis.data.go.kr/6410000"
const maxBody = 2 << 20

// Error never includes request URLs, keys, remote messages, or response bodies.
type Error struct {
	Kind       string `json:"kind"`
	HTTPStatus int    `json:"http_status,omitempty"`
	APICode    string `json:"api_code,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("GBIS 오류: %s (HTTP=%d, API=%s)", e.Kind, e.HTTPStatus, e.APICode)
}

type Client struct {
	base, key  string
	http       *http.Client
	interval   time.Duration
	maxCalls   int
	validation *validationProfile
	mu         sync.Mutex
	calls      int
	last       time.Time
}

func New(key string) (*Client, error) {
	return newClient(BaseURL, key, &http.Client{Timeout: 20 * time.Second}, 2*time.Second, 4)
}

// NewLimited bounds all attempts, including failures. It never retries.
func NewLimited(key string, maxCalls int) (*Client, error) {
	if maxCalls < 1 || maxCalls > 3 {
		return nil, errors.New("요청 상한은 1~3이어야 합니다")
	}
	return newClient(BaseURL, key, &http.Client{Timeout: 20 * time.Second}, 2*time.Second, maxCalls)
}

func newClient(base, key string, h *http.Client, interval time.Duration, maxCalls int) (*Client, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("GBIS_API_KEY 환경변수가 없습니다")
	}
	if strings.Contains(key, "%") {
		var err error
		key, err = url.PathUnescape(key)
		if err != nil {
			return nil, errors.New("GBIS_API_KEY 인코딩 오류")
		}
	}
	hc := *h
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: base, key: key, http: &hc, interval: interval, maxCalls: maxCalls}, nil
}
func (c *Client) Calls() int { c.mu.Lock(); defer c.mu.Unlock(); return c.calls }

type node struct {
	XMLName  xml.Name
	Text     string `xml:",chardata"`
	Children []node `xml:",any"`
}

func (n node) child(name string) (node, bool) {
	for _, v := range n.Children {
		if v.XMLName.Local == name {
			return v, true
		}
	}
	return node{}, false
}
func (n node) val(name string) string { v, _ := n.child(name); return strings.TrimSpace(v.Text) }
func (n node) fields() []string {
	a := make([]string, 0, len(n.Children))
	for _, v := range n.Children {
		a = append(a, v.XMLName.Local)
	}
	sort.Strings(a)
	return a
}
func code(s string) string {
	if len(s) > 4 {
		return "unknown"
	}
	if _, e := strconv.Atoi(s); e != nil {
		return "unknown"
	}
	return s
}
func id(s string) bool { v, e := strconv.ParseUint(s, 10, 64); return e == nil && v > 0 }
func optionalInt(n node, k string) (*int, error) {
	s := n.val(k)
	if s == "" {
		return nil, nil
	}
	v, e := strconv.Atoi(s)
	if e != nil || v < -1 {
		return nil, &Error{Kind: "invalid_numeric_field"}
	}
	if v == -1 {
		return nil, nil
	}
	return &v, nil
}
func requiredInt(n node, k string) (int, error) {
	v, e := optionalInt(n, k)
	if e != nil {
		return 0, e
	}
	if v == nil || *v <= 0 {
		return 0, &Error{Kind: "missing_required_field"}
	}
	return *v, nil
}

func (c *Client) get(ctx context.Context, op string, q url.Values, element string) ([]node, error) {
	// Serialize requests and space their start times. No automatic retries.
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return nil, &Error{Kind: "cancelled"}
	}
	if c.validation != nil {
		if e := c.validation.allow(op, q); e != nil {
			return nil, e
		}
	}
	if c.calls >= c.maxCalls {
		return nil, &Error{Kind: "request_limit"}
	}
	wait := time.Until(c.last.Add(c.interval))
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return nil, &Error{Kind: "cancelled"}
		case <-t.C:
		}
	}
	if c.validation != nil && c.validation.before != nil {
		if e := c.validation.before(ctx); e != nil {
			return nil, e
		}
	}
	q.Set("serviceKey", c.key)
	q.Set("format", "xml")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+op+"?"+q.Encode(), nil)
	if err != nil {
		return nil, &Error{Kind: "request_configuration"}
	}
	c.calls++
	c.last = time.Now()
	started := c.last
	res, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Kind: "transport_or_timeout"}
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return nil, &Error{Kind: "body_read", HTTPStatus: res.StatusCode}
	}
	if len(b) > maxBody {
		return nil, &Error{Kind: "response_too_large", HTTPStatus: res.StatusCode}
	}
	if c.validation != nil {
		if e := c.validation.captureBody(c.key, op, q, started, time.Now(), res.StatusCode, b); e != nil {
			return nil, e
		}
	}
	return parse(b, res.StatusCode, element)
}
func parse(b []byte, status int, element string) ([]node, error) {
	var root node
	err := xml.Unmarshal(b, &root)
	if err == nil && root.XMLName.Local == "OpenAPI_ServiceResponse" {
		h, _ := root.child("cmmMsgHeader")
		return nil, &Error{Kind: "gateway_auth_or_service", HTTPStatus: status, APICode: code(h.val("returnReasonCode"))}
	}
	if status != http.StatusOK {
		return nil, &Error{Kind: "http", HTTPStatus: status}
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, &Error{Kind: "empty_response"}
	}
	if err != nil || root.XMLName.Local != "response" {
		return nil, &Error{Kind: "invalid_xml_schema"}
	}
	h, ok := root.child("msgHeader")
	if !ok || h.val("resultCode") == "" {
		return nil, &Error{Kind: "missing_api_header"}
	}
	if h.val("resultCode") != "0" {
		return nil, &Error{Kind: "api", APICode: code(h.val("resultCode"))}
	}
	body, ok := root.child("msgBody")
	if !ok {
		return nil, &Error{Kind: "missing_body"}
	}
	rows := []node{}
	for _, v := range body.Children {
		if v.XMLName.Local == element {
			rows = append(rows, v)
		}
	}
	if len(rows) == 0 {
		return nil, &Error{Kind: "no_data"}
	}
	return rows, nil
}

type Route struct {
	ID             string   `json:"route_id"`
	Name           string   `json:"name"`
	Start          string   `json:"start_station"`
	End            string   `json:"end_station"`
	ObservedFields []string `json:"observed_fields"`
}
type Station struct {
	ID             string   `json:"station_id"`
	Name           string   `json:"name"`
	Order          int      `json:"station_order"`
	Region         string   `json:"region"`
	Turn           string   `json:"turn"`
	TurnOrder      *int     `json:"turn_order"`
	ObservedFields []string `json:"observed_fields"`
}
type Vehicle struct {
	VehicleID  *string `json:"vehicle_id"`
	PlateNo    *string `json:"plate_no"`
	Minutes    *int    `json:"eta_minutes"`
	Seconds    *int    `json:"eta_seconds"`
	StopsAway  *int    `json:"stops_away"`
	Seats      *int    `json:"remaining_seats"`
	SeatStatus string  `json:"seat_status"`
}
type Arrival struct {
	RouteID        string    `json:"route_id"`
	StationID      string    `json:"station_id"`
	Order          int       `json:"station_order"`
	Flag           string    `json:"flag"`
	Destination    string    `json:"destination"`
	QueryTime      string    `json:"-"`
	Vehicles       []Vehicle `json:"vehicles"`
	ObservedFields []string  `json:"observed_fields"`
}

func (c *Client) Search(ctx context.Context) (Route, error) {
	rows, e := c.get(ctx, "/busrouteservice/v2/getBusRouteListv2", url.Values{"keyword": {"M5121"}}, "busRouteList")
	if e != nil {
		return Route{}, e
	}
	matches := []Route{}
	for _, n := range rows {
		if strings.EqualFold(n.val("routeName"), "M5121") {
			if !id(n.val("routeId")) {
				return Route{}, &Error{Kind: "missing_route_id"}
			}
			matches = append(matches, Route{n.val("routeId"), n.val("routeName"), n.val("startStationName"), n.val("endStationName"), n.fields()})
		}
	}
	if len(matches) != 1 {
		return Route{}, &Error{Kind: "route_not_unique"}
	}
	return matches[0], nil
}
func (c *Client) Stations(ctx context.Context, routeID string) ([]Station, error) {
	if !id(routeID) {
		return nil, &Error{Kind: "invalid_route_id"}
	}
	rows, e := c.get(ctx, "/busrouteservice/v2/getBusRouteStationListv2", url.Values{"routeId": {routeID}}, "busRouteStationList")
	if e != nil {
		return nil, e
	}
	a := []Station{}
	seen := map[int]bool{}
	for _, n := range rows {
		order, e := requiredInt(n, "stationSeq")
		if e != nil {
			return nil, e
		}
		turn, e := optionalInt(n, "turnSeq")
		if e != nil {
			return nil, e
		}
		if !id(n.val("stationId")) || n.val("stationName") == "" || seen[order] {
			return nil, &Error{Kind: "invalid_station_schema"}
		}
		seen[order] = true
		a = append(a, Station{n.val("stationId"), n.val("stationName"), order, n.val("regionName"), n.val("turnYn"), turn, n.fields()})
	}
	sort.Slice(a, func(i, j int) bool { return a[i].Order < a[j].Order })
	return a, nil
}

// SelectSohwa requires a Suwon station followed by a Seoul station on the
// same outbound leg before the documented turning point. Ambiguity fails closed.
func SelectSohwa(a []Station) (Station, error) {
	turn := 0
	for _, s := range a {
		if s.Turn == "Y" {
			if turn != 0 && turn != s.Order {
				return Station{}, &Error{Kind: "ambiguous_turn"}
			}
			turn = s.Order
		}
		if s.TurnOrder != nil && *s.TurnOrder > 0 {
			if turn != 0 && turn != *s.TurnOrder {
				return Station{}, &Error{Kind: "ambiguous_turn"}
			}
			turn = *s.TurnOrder
		}
	}
	found := []Station{}
	for _, s := range a {
		if !strings.Contains(s.Name, "소화초등학교") || !strings.Contains(s.Region, "수원") || turn == 0 || s.Order >= turn {
			continue
		}
		for _, next := range a {
			if next.Order > s.Order && next.Order <= turn && strings.Contains(next.Region, "서울") {
				found = append(found, s)
				break
			}
		}
	}
	if len(found) != 1 {
		return Station{}, &Error{Kind: "seoul_direction_not_verified"}
	}
	return found[0], nil
}
func (c *Client) Arrival(ctx context.Context, routeID string, s Station) (Arrival, error) {
	if !id(routeID) || !id(s.ID) || s.Order <= 0 {
		return Arrival{}, &Error{Kind: "invalid_arrival_parameters"}
	}
	rows, e := c.get(ctx, "/busarrivalservice/v2/getBusArrivalItemv2", url.Values{"routeId": {routeID}, "stationId": {s.ID}, "staOrder": {strconv.Itoa(s.Order)}}, "busArrivalItem")
	if e != nil {
		return Arrival{}, e
	}
	return arrivalRows(rows, routeID, s)
}

func arrivalRows(rows []node, routeID string, s Station) (Arrival, error) {
	if len(rows) != 1 {
		return Arrival{}, &Error{Kind: "ambiguous_arrival"}
	}
	n := rows[0]
	order, e := requiredInt(n, "staOrder")
	if e != nil {
		return Arrival{}, e
	}
	if n.val("routeId") != routeID || n.val("stationId") != s.ID || order != s.Order {
		return Arrival{}, &Error{Kind: "arrival_identity_mismatch"}
	}
	out := Arrival{RouteID: routeID, StationID: s.ID, Order: order, Flag: n.val("flag"), Destination: n.val("routeDestName"), Vehicles: []Vehicle{}, ObservedFields: n.fields()}
	for i := 1; i <= 2; i++ {
		suffix := strconv.Itoa(i)
		v := Vehicle{SeatStatus: "unknown"}
		for _, f := range []struct {
			name string
			dest **int
		}{{"predictTime", &v.Minutes}, {"predictTimeSec", &v.Seconds}, {"locationNo", &v.StopsAway}, {"remainSeatCnt", &v.Seats}} {
			x, e := optionalInt(n, f.name+suffix)
			if e != nil {
				return Arrival{}, e
			}
			*f.dest = x
		}
		if x := n.val("vehId" + suffix); id(x) {
			v.VehicleID = &x
		} else if x != "" && x != "-1" && x != "0" {
			return Arrival{}, &Error{Kind: "invalid_vehicle_id"}
		}
		if x := n.val("plateNo" + suffix); x != "" && x != "-1" {
			v.PlateNo = &x
		}
		if v.Seats != nil {
			v.SeatStatus = "provided"
		}
		out.Vehicles = append(out.Vehicles, v)
	}
	return out, nil
}

// ParseArrivalXML reuses the live parser without constructing an HTTP client.
func ParseArrivalXML(raw []byte, status int, route string, s Station) (Arrival, error) {
	if len(raw) > maxBody || !id(route) || !id(s.ID) || s.Order <= 0 {
		return Arrival{}, &Error{Kind: "invalid_recorded_arrival"}
	}
	rows, e := parse(raw, status, "busArrivalItem")
	if e != nil {
		return Arrival{}, e
	}
	return arrivalRows(rows, route, s)
}
