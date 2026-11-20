package alternatives

import (
	"context"
	"errors"
	"geoseulleong/internal/odsay"
)

type ExpressSegment struct {
	Status      string   `json:"status"`
	ReasonCode  string   `json:"reason_code,omitempty"`
	GBISRouteID string   `json:"gbis_route_id"`
	BoardingID  string   `json:"boarding_gbis_stop_id"`
	AlightingID string   `json:"alighting_gbis_stop_id"`
	Minutes     *float64 `json:"duration_minutes"`
	DataOrigin  string   `json:"data_origin"`
	leg         *odsay.SubPath
}

func alightingVisit(original Visit, leg odsay.SubPath, data Snapshot) (Visit, bool) {
	if leg.EndProvider == nil || *leg.EndProvider != 2 || leg.EndLocal == "" {
		return Visit{}, false
	}
	matches := []Visit{}
	for _, v := range data.Visits {
		if v.GBIS == leg.EndLocal && v.Sequence > original.Sequence && v.TurnSequence != nil && original.TurnSequence != nil && *v.TurnSequence == *original.TurnSequence && (direction(v) == direction(original) || direction(v) == "turn") {
			matches = append(matches, v)
		}
	}
	if len(matches) != 1 {
		return Visit{}, false
	}
	return matches[0], true
}

// Filter before any optional provider requests; indices and existing route ranks
// are unchanged, and invalid candidates retain a concise excluded status.
func validateCandidateDestination(out *Result, leg odsay.SubPath, data Snapshot) {
	end, valid := alightingVisit(out.Original, leg, data)
	for i := range out.Candidates {
		c := &out.Candidates[i]
		reason := ""
		if !valid {
			reason = "alighting_visit_unknown"
		} else if c.Visit.Sequence >= end.Sequence {
			reason = "candidate_at_or_after_alighting"
		}
		if reason != "" {
			c.Status = "excluded"
			c.Reasons = appendUnique(c.Reasons, reason)
		}
	}
}
func storeSegment(result *ExpressSegment, leg *odsay.SubPath, origin string) {
	result.Status = "available"
	result.ReasonCode = ""
	result.DataOrigin = origin
	value := *leg.Minutes
	result.Minutes = &value
	copy := *leg
	copy.Minutes = &value
	result.leg = &copy
}

// EnrichExpress reuses exact saved sections first, then uses the same guarded
// ODsay planner/cache as access search. Only the matched bus section is retained:
// provider totalTime, query walking and other buses are never added to the trip.
func EnrichExpress(ctx context.Context, out *Result, selected odsay.SubPath, paths []odsay.Path, route odsay.Lane, data Snapshot, planner *AccessPlanner, saved *odsay.Selections, origin string, recordings ...[]odsay.Selection) {
	if origin == "" {
		origin = "odsay_recorded_response"
	}
	end, endOK := alightingVisit(out.Original, selected, data)
	snapshots := saved.Snapshots()
	for _, recording := range recordings {
		snapshots = append(snapshots, recording...)
	}
	stop := false
	for i := range out.Candidates {
		c := &out.Candidates[i]
		result := &ExpressSegment{Status: "unavailable", GBISRouteID: route.LocalID, BoardingID: c.Visit.GBIS, AlightingID: selected.EndLocal, DataOrigin: "unavailable"}
		c.ExpressSegment = result
		if i >= 3 {
			result.ReasonCode = "segment_request_cap"
			continue
		}
		if c.Status == "excluded" || !endOK || direction(c.Visit) != direction(out.Original) || c.Visit.Sequence >= end.Sequence || c.Visit.TurnSequence == nil || end.TurnSequence == nil || *c.Visit.TurnSequence != *end.TurnSequence {
			result.ReasonCode = "segment_direction_unverified"
			continue
		}
		matches := 0
		for _, v := range data.Visits {
			if v.GBIS == c.Visit.GBIS && direction(v) == direction(c.Visit) {
				matches++
			}
		}
		if matches != 1 {
			result.ReasonCode = "segment_visit_ambiguous"
			continue
		}
		known, conflict := exactExpress(paths, route, c.Visit.GBIS, end.GBIS)
		if conflict {
			result.ReasonCode = "bus_time_conflict"
			continue
		}
		if known != nil {
			storeSegment(result, known, origin)
			continue
		}
		for _, snapshot := range snapshots {
			known, conflict = exactExpress(snapshot.Response.Paths, route, c.Visit.GBIS, end.GBIS)
			if conflict {
				continue
			}
			if known != nil {
				storeSegment(result, known, snapshot.Origin)
				break
			}
		}
		if result.leg != nil {
			continue
		}
		if stop || ctx.Err() != nil {
			result.ReasonCode = "segment_not_attempted"
			continue
		}
		if planner == nil || planner.Searcher == nil {
			result.ReasonCode = "segment_lookup_disabled"
			continue
		}
		point := end.Point
		if !validPoint(point) {
			point = pointOf(selected.EndX, selected.EndY)
		}
		if !validPoint(c.Visit.Point) || !validPoint(point) {
			result.ReasonCode = "segment_coordinates_missing"
			continue
		}
		query := odsay.Query{Start: *c.Visit.Point, End: *point, Mode: "all"}
		if !query.Valid() {
			result.ReasonCode = "segment_coordinates_invalid"
			continue
		}
		response, source, _, e := planner.lookup(ctx, query)
		if e != nil {
			result.ReasonCode = "segment_provider_unavailable"
			switch {
			case errors.Is(e, odsay.ErrBudget):
				result.ReasonCode = "segment_budget_exhausted"
				stop = true
			case errors.Is(e, odsay.ErrState):
				result.ReasonCode = "segment_budget_unavailable"
				stop = true
			case errors.Is(e, context.DeadlineExceeded) || errors.Is(e, odsay.ErrTimeout):
				result.ReasonCode = "segment_timeout"
			}
			if ctx.Err() != nil {
				stop = true
			}
			continue
		}
		known, conflict = exactExpress(response.Paths, route, c.Visit.GBIS, end.GBIS)
		if conflict {
			result.ReasonCode = "bus_time_conflict"
			continue
		}
		if known == nil {
			result.ReasonCode = "segment_not_found"
			continue
		}
		storeSegment(result, known, source)
	}
}
