package alternatives

import (
	"context"
	"geoseulleong/internal/gbis"
	"geoseulleong/internal/odsay"
	"geoseulleong/internal/reachability"
	"strconv"
	"time"
)

func estimate(a *AccessResult) reachability.Access {
	out := reachability.Access{}
	if a == nil {
		return out
	}
	p := bestAccessPath(a)
	if p == nil {
		return out
	}
	seconds := p.Minutes * 60
	out.Seconds = &seconds
	out.DestinationValidated = p.ArrivalStatus == "stop_id_and_coordinates_match"
	if p.Timing != nil {
		out.Coverage = *p.Timing
	}
	return out
}
func originalAccess(ctx context.Context, out *Result, origin *odsay.Point, route odsay.Lane, prefix []odsay.SubPath, captured *time.Time, planner *AccessPlanner) {
	// Reuse the known original pre-boarding sections. Do not spend an extra
	// ODsay request outside the three-candidate budget.
	// Existing pre-boarding sections are a lower-bound reference, not a made-up zero.
	prior := out.OriginalAccess
	a := emptyAccess("recorded_prefix_time_incomplete")
	if prior != nil {
		a.Query = prior.Query
		a.Error = prior.Error
	}
	a.Origin = origin
	a.DataOrigin = "odsay_recorded_preboarding_sections"
	p := AccessPath{ArrivalStatus: "prefix_endpoint_unverified", OriginStatus: "recorded_request_bound", Legs: []AccessLeg{}}
	walking := true
	p.Timing = &reachability.Coverage{Walking: &walking, CapturedAt: captured, Evidence: "recorded_preboarding_section_sum_incomplete"}
	if len(prefix) == 0 {
		a.Status = "original_access_time_unavailable"
		out.OriginalAccess = a
		return
	}
	transit := 0
	for _, l := range prefix {
		if l.Minutes == nil || *l.Minutes < 0 {
			a.Status = "original_access_time_unavailable"
			out.OriginalAccess = a
			return
		}
		p.Minutes += *l.Minutes
		kind := "walk"
		if l.TrafficType == 2 {
			transit++
			kind = "bus"
		}
		if l.TrafficType == 1 {
			transit++
			kind = "subway"
		}
		p.Legs = append(p.Legs, AccessLeg{Type: kind, Minutes: *l.Minutes, Segment: l})
		if l.TrafficType == 3 {
			p.WalkingMinutes += *l.Minutes
		}
	}
	p.Transfers = max(0, transit-1)
	for i := len(prefix) - 1; i >= 0 && prefix[i].TrafficType == 3; i-- {
		p.FinalWalkMinutes += *prefix[i].Minutes
	}
	a.Uncertain = append(a.Uncertain, p)
	out.OriginalAccess = a
}
func EnrichFeasibility(ctx context.Context, out *Result, data Snapshot, origin *odsay.Point, route odsay.Lane, _ odsay.SubPath, prefix []odsay.SubPath, captured *time.Time, planner *AccessPlanner, provider reachability.Provider, clock func() time.Time) {
	now := time.Now()
	if clock != nil {
		now = clock()
	}
	originalAccess(ctx, out, origin, route, prefix, captured, planner)
	if provider != nil {
		if _, offline := provider.(*reachability.Replay); !offline {
			out.Policy["external_api_calls"] = nil
			out.Policy["realtime_transmission_count_source"] = "injected_GBIS_provider_budget"
		}
	}
	out.Policy["feasibility_scope"] = "matched_reported_vehicles_only_if_depart_now; not_boarding_success"
	out.Policy["feasibility_odds_or_combined_time"] = "not_computed"
	out.Policy["future_gbis_query_upper_bound"] = 1 + 1 + min(3, len(out.Candidates))
	out.Policy["future_access_query_upper_bound"] = min(3, len(out.Candidates))
	visits := map[int]string{}
	for _, v := range data.Visits {
		visits[v.Sequence] = v.GBIS
	}
	var locations reachability.LocationsSnapshot
	var locErr error
	arrivals := map[string]reachability.ArrivalSnapshot{}
	arrivalErrors := map[string]error{}
	if provider != nil {
		targets := []Visit{out.Original}
		for i, c := range out.Candidates {
			if i < 3 && c.Status != "excluded" {
				targets = append(targets, c.Visit)
			}
		}
		// Place the single Locations query between Arrivals to minimize observation
		// skew without spending extra requests or relaxing timestamp validation.
		split := (len(targets) + 1) / 2
		for i, v := range targets {
			if i == split {
				locations, locErr = provider.Locations(ctx, data.GBIS)
			}
			key := v.GBIS + "/" + strconv.Itoa(v.Sequence)
			arrivals[key], arrivalErrors[key] = provider.Arrival(ctx, data.GBIS, gbis.Station{ID: v.GBIS, Order: v.Sequence})
		}
		if split == len(targets) {
			locations, locErr = provider.Locations(ctx, data.GBIS)
		}
	}

	evaluate := func(v Visit, a *AccessResult) *reachability.Result {
		access := estimate(a)
		unknown := func(reason string) *reachability.Result { r := reachability.Unknown(access, now, reason); return &r }
		if provider == nil {
			return unknown("realtime_provider_disabled")
		}
		if locErr != nil {
			return unknown("locations_unavailable")
		}
		if v.TurnSequence == nil {
			return unknown("turn_sequence_unknown")
		}
		key := v.GBIS + "/" + strconv.Itoa(v.Sequence)
		snap, e := arrivals[key], arrivalErrors[key]
		if e != nil {
			return unknown("arrival_unavailable")
		}
		evaluatedAt := time.Now()
		if clock != nil {
			evaluatedAt = clock()
		}
		r := reachability.Evaluate(reachability.Stop{Route: data.GBIS, ID: v.GBIS, Order: v.Sequence, Turn: *v.TurnSequence, BoardingAllowed: v.Allowed}, visits, access, snap, locations, evaluatedAt)
		return &r
	}
	out.OriginalReachability = evaluate(out.Original, out.OriginalAccess)
	for i := range out.Candidates {
		if i >= 3 {
			r := reachability.Unknown(estimate(out.Candidates[i].AccessPaths), now, "candidate_cap")
			out.Candidates[i].Reachability = &r
			continue
		}
		c := &out.Candidates[i]
		if c.Status == "excluded" {
			r := reachability.Unknown(estimate(c.AccessPaths), now, "candidate_excluded")
			c.Reachability = &r
			continue
		}
		c.Reachability = evaluate(c.Visit, c.AccessPaths)
	}
}
