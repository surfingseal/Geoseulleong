package alternatives

import (
	"context"
	"errors"
	"geoseulleong/internal/odsay"
	"geoseulleong/internal/reachability"
	"geoseulleong/internal/stopmapping"
	"math"
	"sync"
	"time"
)

// AccessPlanner borrows the existing server-wide ODsay Live searcher. It owns no
// credentials, cache or retry policy. Recorded searchers are used offline.
type AccessPlanner struct {
	Searcher odsay.Searcher
	mu       sync.Mutex
	cache    map[odsay.Query]cachedAccess
}
type cachedAccess struct {
	response odsay.Response
	origin   string
	timing   *reachability.Coverage
	err      error
	at       time.Time
}

// A small shared cache also deduplicates recorded/mock providers. Live's existing
// durable budget/cache remains authoritative; timestamps are never refreshed.
func (p *AccessPlanner) lookup(ctx context.Context, q odsay.Query) (odsay.Response, string, *reachability.Coverage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return odsay.Response{}, "", nil, e
	}
	now := time.Now()
	if v, ok := p.cache[q]; ok && now.Sub(v.at) < 30*time.Second {
		return v.response, v.origin, v.timing, v.err
	}
	response, origin, e := p.Searcher.Search(ctx, q)
	var timing *reachability.Coverage
	if timed, ok := p.Searcher.(interface {
		TimingFor(odsay.Query) *reachability.Coverage
	}); ok {
		timing = timed.TimingFor(q)
	}
	if p.cache == nil {
		p.cache = map[odsay.Query]cachedAccess{}
	}
	if len(p.cache) >= 128 {
		for k, v := range p.cache {
			if now.Sub(v.at) >= 30*time.Second {
				delete(p.cache, k)
			}
		}
	}
	if len(p.cache) < 128 {
		p.cache[q] = cachedAccess{response, origin, timing, e, time.Now()}
	}
	return response, origin, timing, e
}

type AccessResult struct {
	Status       string            `json:"status"`
	Origin       *odsay.Point      `json:"user_origin,omitempty"`
	OriginSource string            `json:"origin_source"`
	Query        *odsay.Query      `json:"query,omitempty"`
	DataOrigin   string            `json:"data_origin,omitempty"`
	Paths        []AccessPath      `json:"paths"`
	Uncertain    []AccessPath      `json:"uncertain_paths"`
	Rejected     []AccessRejection `json:"rejected_paths"`
	Error        string            `json:"error_code,omitempty"`
}
type AccessLeg struct {
	Type    string        `json:"type"`
	Minutes float64       `json:"duration_minutes"`
	Segment odsay.SubPath `json:"provider_segment"`
}
type AccessPath struct {
	Timing            *reachability.Coverage `json:"time_coverage,omitempty"`
	Index             int                    `json:"provider_index"`
	Minutes           float64                `json:"provider_total_minutes"`
	Transfers         int                    `json:"derived_transfer_count"`
	WalkingMinutes    float64                `json:"walking_minutes"`
	FinalWalkMinutes  float64                `json:"final_walk_minutes"`
	ArrivalStatus     string                 `json:"arrival_status"`
	DestinationMetres *float64               `json:"destination_distance_metres"`
	OriginStatus      string                 `json:"origin_validation"`
	Legs              []AccessLeg            `json:"legs"`
	Confirmed         bool                   `json:"confirmed_recommendation"`
}
type AccessRejection struct {
	Index  int    `json:"provider_index"`
	Reason string `json:"reason"`
}

func emptyAccess(status string) *AccessResult {
	return &AccessResult{Status: status, OriginSource: "recorded_user_request_coordinates", Paths: []AccessPath{}, Uncertain: []AccessPath{}, Rejected: []AccessRejection{}}
}
func validPoint(p *odsay.Point) bool {
	return p != nil && !math.IsNaN(p.Lon) && !math.IsNaN(p.Lat) && !math.IsInf(p.Lon, 0) && !math.IsInf(p.Lat, 0) && p.Lon >= -180 && p.Lon <= 180 && p.Lat >= -90 && p.Lat <= 90
}
func pointOf(x, y *float64) *odsay.Point {
	if x == nil || y == nil {
		return nil
	}
	p := &odsay.Point{Lon: *x, Lat: *y}
	if !validPoint(p) {
		return nil
	}
	return p
}
func EnrichAccess(ctx context.Context, out *Result, origin *odsay.Point, targetRoute odsay.Lane, planner *AccessPlanner) {
	if out.Policy == nil {
		out.Policy = map[string]any{}
	}
	out.Policy["access_routes_requested"] = true
	out.Policy["access_max_queries"] = min(3, len(out.Candidates))
	out.Policy["access_cost_policy"] = "shared_ODsay_Live_ledger_cache_singleflight; no_retry; max_3_searches"
	out.Policy["access_time_policy"] = "provider_estimate_only_no_wait_or_combined_journey_time"
	stop := false
	for i := range out.Candidates {
		c := &out.Candidates[i]
		if i >= 3 {
			c.AccessPaths = emptyAccess("request_cap_exceeded")
			continue
		}
		a := emptyAccess("disabled")
		c.AccessPaths = a
		if c.Status == "excluded" {
			a.Status = "candidate_excluded"
			continue
		}
		if stop {
			a.Status = "not_attempted_budget_or_context"
			continue
		}
		if targetRoute.Provider == nil || *targetRoute.Provider != 2 || targetRoute.LocalID == "" || targetRoute.BusNumber == "" {
			a.Status = "target_route_identity_unverified"
			continue
		}
		if c.Visit.ID <= 0 || c.Visit.GBIS == "" {
			a.Status = "candidate_identity_unverified"
			continue
		}
		if !validPoint(origin) {
			a.Status = "user_origin_unavailable"
			continue
		}
		a.Origin = origin
		if planner == nil || planner.Searcher == nil {
			continue
		}
		if !validPoint(c.Visit.Point) {
			a.Status = "candidate_coordinates_unavailable"
			continue
		}
		if direction(c.Visit) == "unknown" || direction(c.Visit) != direction(out.Original) || c.Visit.TurnSequence == nil || out.Original.TurnSequence == nil || *c.Visit.TurnSequence != *out.Original.TurnSequence {
			a.Status = "direction_conflict"
			continue
		}
		if c.Visit.Allowed != nil && !*c.Visit.Allowed {
			a.Status = "boarding_not_allowed"
			continue
		}
		q := odsay.Query{Start: *origin, End: *c.Visit.Point, Mode: "all"}
		a.Query = &q
		if !q.Valid() {
			a.Status = "invalid_coordinates_or_same_point"
			continue
		}
		// One query per candidate, sequentially; Live handles global reservations,
		// request spacing, persistent limits, caching and in-flight deduplication.
		response, source, providedTiming, e := planner.lookup(ctx, q)
		a.DataOrigin = source
		if e != nil {
			a.Status = "unavailable"
			a.Error = "provider_error"
			if errors.Is(e, odsay.ErrBudget) {
				a.Error = "budget_exhausted"
			}
			if errors.Is(e, odsay.ErrDisabled) {
				a.Error = "provider_disabled"
			}
			if errors.Is(e, context.DeadlineExceeded) || errors.Is(e, odsay.ErrTimeout) {
				a.Error = "timeout"
			}
			if errors.Is(e, odsay.ErrBudget) || errors.Is(e, odsay.ErrState) || ctx.Err() != nil {
				stop = true
			}
			continue
		}
		for index, p := range response.Paths {
			path, reason := evaluateAccessPath(index, p, *origin, c.Visit, targetRoute)
			walking := true
			coverage := reachability.Coverage{Walking: &walking, Evidence: "provider_sections_only_wait_and_connections_unverified"}
			if providedTiming != nil {
				coverage = *providedTiming
			}
			path.Timing = &coverage
			if reason != "" {
				a.Rejected = append(a.Rejected, AccessRejection{index, reason})
			} else if path.ArrivalStatus == "stop_id_and_coordinates_match" {
				a.Paths = append(a.Paths, path)
			} else {
				a.Uncertain = append(a.Uncertain, path)
			}
		}
		a.Status = "no_usable_path"
		if len(a.Uncertain) > 0 {
			a.Status = "arrival_unverified"
		}
		if len(a.Paths) > 0 {
			a.Status = "access_path_available"
		}
	}
	for i := range out.Candidates {
		c := &out.Candidates[i]
		if c.Access == nil {
			c.Access = map[string]any{}
		}
		if c.AccessPaths != nil {
			c.Access["status"] = c.AccessPaths.Status
		}
	}
	if planner != nil && planner.Searcher != nil {
		if _, offline := planner.Searcher.(*AccessReplay); !offline {
			out.Policy["external_api_calls"] = nil
			out.Policy["transmission_count_source"] = "upstream_shared_ledger_or_mock_not_search_invocation_count"
		}
	}

}
func evaluateAccessPath(index int, p odsay.Path, origin odsay.Point, target Visit, route odsay.Lane) (AccessPath, string) {
	a := AccessPath{Index: index, ArrivalStatus: "endpoint_unverified", OriginStatus: "request_bound_origin_not_echoed", Legs: []AccessLeg{}}
	if p.Info.TotalTime == nil || *p.Info.TotalTime < 0 || math.IsNaN(*p.Info.TotalTime) || math.IsInf(*p.Info.TotalTime, 0) || len(p.Legs) == 0 {
		return a, "invalid_provider_path"
	}
	a.Minutes = *p.Info.TotalTime
	transit := 0
	lastTransit := -1
	for i, l := range p.Legs {
		if l.Minutes == nil || *l.Minutes < 0 || math.IsNaN(*l.Minutes) || math.IsInf(*l.Minutes, 0) {
			return a, "invalid_leg_duration"
		}
		kind := "walk"
		switch l.TrafficType {
		case 1:
			kind = "subway"
			transit++
			lastTransit = i
		case 2:
			kind = "bus"
			transit++
			lastTransit = i
			for _, v := range l.Lanes {
				if v.BusNumber == route.BusNumber && route.BusNumber != "" || v.Provider != nil && route.Provider != nil && *v.Provider == *route.Provider && v.LocalID != "" && v.LocalID == route.LocalID {
					return a, "target_express_bus_before_candidate"
				}
			}
		case 3:
			a.WalkingMinutes += *l.Minutes
		default:
			return a, "unknown_leg_type"
		}
		a.Legs = append(a.Legs, AccessLeg{kind, *l.Minutes, l})
	}
	a.Transfers = max(0, transit-1)
	for i := len(p.Legs) - 1; i >= 0 && p.Legs[i].TrafficType == 3; i-- {
		a.FinalWalkMinutes += *p.Legs[i].Minutes
	}
	first := p.Legs[0]
	if start := pointOf(first.StartX, first.StartY); start != nil {
		if straightLine(origin, *start) > 100 {
			return a, "origin_coordinate_conflict"
		}
		a.OriginStatus = "coordinates_within_100m"
	}
	last := p.Legs[len(p.Legs)-1]
	end := pointOf(last.EndX, last.EndY)
	terminalID := last.EndLocal != "" && last.EndProvider != nil && *last.EndProvider == 2
	if terminalID && last.EndLocal != target.GBIS {
		return a, "destination_stop_id_conflict"
	}
	if end != nil {
		d := straightLine(*end, *target.Point)
		a.DestinationMetres = &d
		if d > 100 {
			return a, "destination_coordinate_conflict"
		}
	}
	if terminalID && last.EndLocal == target.GBIS && end != nil {
		if last.EndName != "" && stopmapping.CompareName(last.EndName) != stopmapping.CompareName(target.Name) {
			a.ArrivalStatus = "stop_name_conflict"
			return a, ""
		}
		a.ArrivalStatus = "stop_id_and_coordinates_match"
		return a, ""
	}
	if last.TrafficType == 3 {
		a.ArrivalStatus = "last_walk_endpoint_unverified"
		if end != nil {
			a.ArrivalStatus = "spatial_candidate_no_terminal_stop_id"
		}
		// A transit alighting ID cannot prove the final walking endpoint. Preserve
		// its evidence but never synthesize a final ID, duration or coordinate.
		if lastTransit >= 0 {
			l := p.Legs[lastTransit]
			if l.EndLocal == target.GBIS && l.EndProvider != nil && *l.EndProvider == 2 && end == nil {
				a.ArrivalStatus = "matching_alighting_id_last_walk_geometry_missing"
			}
		}
	} else if end != nil {
		a.ArrivalStatus = "spatial_candidate_no_terminal_stop_id"
	} else {
		a.ArrivalStatus = "arrival_coordinates_unavailable"
	}
	return a, ""
}
