package alternatives

import (
	"geoseulleong/internal/odsay"
	"sort"
)

// Comparison keeps access estimates distinct from the unqueried remainder of a
// trip. No total alternative duration or probability is invented.
type JourneySummary struct {
	DataOrigin      string         `json:"data_origin"`
	ReferenceOnly   bool           `json:"reference_only"`
	DurationMinutes *float64       `json:"duration_minutes"`
	Transfers       int            `json:"transfers"`
	WalkingMinutes  float64        `json:"walking_minutes"`
	Access          *AccessMetrics `json:"access"`
	Congestion      Profile        `json:"congestion"`
}
type AccessMetrics struct {
	ProviderIndex     int     `json:"provider_index"`
	DurationMinutes   float64 `json:"duration_minutes"`
	Transfers         int     `json:"transfers"`
	WalkingMinutes    float64 `json:"walking_minutes"`
	FinalWalkMinutes  float64 `json:"final_walk_minutes"`
	DestinationStatus string  `json:"destination_status"`
}
type Comparison struct {
	TotalComparisonStatus    string         `json:"total_comparison_status"`
	TotalWalkingMinutes      *float64       `json:"total_walking_minutes"`
	TotalTransfers           *int           `json:"total_transfer_count"`
	TotalMinutesDifference   *float64       `json:"total_minutes_difference"`
	TotalWalkingDifference   *float64       `json:"total_walking_difference"`
	TotalTransferDifference  *int           `json:"total_transfer_difference"`
	TotalTimeBasis           string         `json:"total_time_basis"`
	Access                   *AccessMetrics `json:"access"`
	AccessMinutesDifference  *float64       `json:"access_minutes_difference"`
	TransferDifference       *int           `json:"access_transfer_difference"`
	WalkingMinutesDifference *float64       `json:"access_walking_minutes_difference"`
	MeanCGSTDifference       *float64       `json:"mean_cgst_difference"`
	P90CGSTDifference        *float64       `json:"p90_cgst_difference"`
	TotalJourneyMinutes      *float64       `json:"total_journey_minutes"`
	TotalJourneyStatus       string         `json:"total_journey_status"`
	Reachability             string         `json:"reachability"`
	BoardingStatus           string         `json:"boarding_status"`
}

func bestAccessPath(a *AccessResult) *AccessPath {
	if a == nil {
		return nil
	}
	paths := append(append([]AccessPath{}, a.Paths...), a.Uncertain...)
	full := func(p AccessPath) bool {
		return p.ArrivalStatus == "stop_id_and_coordinates_match" && p.Timing != nil && p.Timing.TransferWait != nil && *p.Timing.TransferWait && p.Timing.Connections != nil && *p.Timing.Connections
	}
	best := -1
	for i, p := range paths {
		if best < 0 || full(p) && !full(paths[best]) || full(p) == full(paths[best]) && p.Minutes < paths[best].Minutes {
			best = i
		}
	}
	if best < 0 {
		return nil
	}
	p := paths[best]
	return &p

}
func bestMetrics(a *AccessResult) *AccessMetrics {
	p := bestAccessPath(a)
	if p == nil {
		return nil
	}
	return &AccessMetrics{ProviderIndex: p.Index, DurationMinutes: p.Minutes, Transfers: p.Transfers, WalkingMinutes: p.WalkingMinutes, FinalWalkMinutes: p.FinalWalkMinutes, DestinationStatus: p.ArrivalStatus}
}
func Summarize(out *Result, p odsay.Path) {
	summary := &JourneySummary{DataOrigin: "odsay_recorded_response", ReferenceOnly: true, DurationMinutes: p.Info.TotalTime, Access: bestMetrics(out.OriginalAccess), Congestion: out.Profile}
	if out.DataOrigin != "" {
		summary.DataOrigin = out.DataOrigin
	}
	transit := 0
	for _, l := range p.Legs {
		if l.TrafficType == 3 && l.Minutes != nil {
			summary.WalkingMinutes += *l.Minutes
		}
		if l.TrafficType == 1 || l.TrafficType == 2 {
			transit++
		}
	}
	summary.Transfers = max(0, transit-1)
	out.OriginalJourney = summary
	out.Policy = map[string]any{"recommendation_method": "lower_cgst_with_access_and_reported_vehicle_feasibility", "max_candidates": 3, "max_odsay_access_queries": 3, "max_gbis_queries": 5, "cgst_reference_only": true, "boarding_success_guaranteed": false, "total_alternative_time": "sum_exact_known_sections_else_null"}
	out.Examined = nil
	for i := range out.Candidates {
		c := &out.Candidates[i]
		c.RecommendationRank = nil
		c.RecommendationStatus = "unknown"
		c.RecommendationReasons = []string{}
		v := &Comparison{Access: bestMetrics(c.AccessPaths), MeanCGSTDifference: c.MeanDifference, P90CGSTDifference: c.P90Difference, TotalJourneyStatus: "incomplete", Reachability: "unknown", BoardingStatus: "unknown"}
		c.Comparison = v
		if v.Access != nil && summary.Access != nil {
			d := v.Access.DurationMinutes - summary.Access.DurationMinutes
			v.AccessMinutesDifference = &d
			t := v.Access.Transfers - summary.Access.Transfers
			v.TransferDifference = &t
			w := v.Access.WalkingMinutes - summary.Access.WalkingMinutes
			v.WalkingMinutesDifference = &w
		}
		if c.Reachability != nil {
			v.Reachability = c.Reachability.Status
			v.BoardingStatus = c.Reachability.BoardingStatus
		}
		switch {
		case c.Status == "excluded":
			c.RecommendationStatus = "excluded"
			c.RecommendationReasons = append(c.RecommendationReasons, c.Reasons...)
		case c.Visit.Allowed != nil && !*c.Visit.Allowed:
			c.RecommendationStatus = "excluded"
			c.RecommendationReasons = append(c.RecommendationReasons, "boarding_not_allowed")
		case v.Reachability == "unreachable":
			c.RecommendationStatus = "unreachable"
			c.RecommendationReasons = append(c.RecommendationReasons, "access_takes_longer_than_reported_eta")
		case v.Reachability != "reachable":
			c.RecommendationReasons = append(c.RecommendationReasons, "realtime_or_access_unavailable")
		case c.Visit.Allowed == nil:
			c.RecommendationReasons = append(c.RecommendationReasons, "boarding_permission_unknown")
		case v.Access == nil || v.Access.DestinationStatus != "stop_id_and_coordinates_match":
			c.RecommendationReasons = append(c.RecommendationReasons, "access_destination_unknown")
		default:
			c.RecommendationStatus = "recommended"
		}
		if c.Profile.LowSample || out.Profile.LowSample {
			c.RecommendationReasons = append(c.RecommendationReasons, "low_sample")
		}
		// Seats remain vehicle-specific. A reachable positive-seat observation is
		// required before labelling the candidate recommended; never infer capacity.
		if c.RecommendationStatus == "recommended" {
			positive := false
			zero := false
			for _, vehicle := range c.Reachability.Vehicles {
				if vehicle.Status != "reachable" || vehicle.SeatStatus == "conflict_preserved" {
					continue
				}
				if vehicle.ArrivalSeats != nil {
					positive = positive || *vehicle.ArrivalSeats > 0
					zero = zero || *vehicle.ArrivalSeats == 0
				}
			}
			if !positive {
				c.RecommendationStatus = "unknown"
				reason := "seats_unavailable"
				if zero {
					reason = "no_seats_reported"
				}
				c.RecommendationReasons = append(c.RecommendationReasons, reason)
			}
		}
		// Preserve the older flag as a non-guarantee; clients use recommendation_status.
		c.Confirmed = false
	}
	ranks := []int{}
	for i, c := range out.Candidates {
		if c.RecommendationStatus == "recommended" {
			ranks = append(ranks, i)
		}
	}
	sort.SliceStable(ranks, func(i, j int) bool {
		a, b := out.Candidates[ranks[i]], out.Candidates[ranks[j]]
		return a.Comparison.Access.DurationMinutes < b.Comparison.Access.DurationMinutes
	})
	for rank, index := range ranks {
		n := rank + 1
		out.Candidates[index].RecommendationRank = &n
	}
}
