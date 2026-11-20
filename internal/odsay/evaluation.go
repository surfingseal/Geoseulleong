package odsay

import (
	"encoding/json"
	"math"
	"sort"
)

// Evaluation compares descriptive metrics. Congestion never modifies provider travel time or rank.
type Evaluation struct {
	TransferSource   string                `json:"transfer_count_source,omitempty"`
	Boarding         *BoardingScore        `json:"boarding_aware,omitempty"`
	Method           string                `json:"method"`
	TravelMinutes    float64               `json:"travel_minutes"`
	Transfers        int                   `json:"transfers"`
	WalkingMinutes   float64               `json:"walking_minutes"`
	ContainsM5121    bool                  `json:"contains_m5121_option"`
	CongestionStatus string                `json:"congestion_status"`
	References       []EvaluationReference `json:"congestion_references"`
}
type EvaluationReference struct {
	LegIndex          int               `json:"leg_index"`
	OptionIndex       int               `json:"option_index"`
	Type              string            `json:"type"`
	BusNumber         string            `json:"bus_no,omitempty"`
	ODsayRouteID      ID                `json:"odsay_route_id,omitempty"`
	LineName          string            `json:"line_name,omitempty"`
	SubwayCode        *int              `json:"subway_code,omitempty"`
	Status            string            `json:"status"`
	Reason            string            `json:"reason"`
	Mean              *float64          `json:"mean_cgst"`
	P90               *float64          `json:"p90"`
	Observations      *int64            `json:"observation_count"`
	NumericCount      *int64            `json:"numeric_count"`
	BoardingVisitID   *int64            `json:"boarding_visit_id"`
	AlightingVisitID  *int64            `json:"alighting_visit_id"`
	ReferenceDate     *string           `json:"reference_date"`
	ReferenceTimeCode *string           `json:"reference_tzon"`
	Policy            map[string]string `json:"policy"`
}

func optionReference(q Query, legIndex, optionIndex int, o BusOption) EvaluationReference {
	r := EvaluationReference{LegIndex: legIndex, OptionIndex: optionIndex, Type: "bus", BusNumber: o.Lane.BusNumber, ODsayRouteID: o.Lane.ODsayID, Status: "unknown", Reason: "no_verified_boarding_statistics"}
	m := o.Mapping
	if !o.M5121Candidate || o.Lane.BusNumber != "M5121" || o.CongestionStatus != "available" || m == nil || m.Status != "reference_available" || m.BoardingStatus != "matched_declared_gbis_local_id" || m.BoardingVisitID == nil || *m.BoardingVisitID <= 0 || m.RouteID == nil || *m.RouteID <= 0 {
		return r
	}
	var v struct {
		Status   string            `json:"status"`
		Mapping  string            `json:"mapping_status"`
		RouteID  int64             `json:"route_id"`
		VisitID  int64             `json:"route_stop_id"`
		Date     string            `json:"service_date"`
		Set      string            `json:"analysis_set_id"`
		TimeCode *string           `json:"tzon"`
		Mean     *float64          `json:"mean_cgst"`
		P90      *float64          `json:"p90"`
		Count    int64             `json:"observation_count"`
		Numeric  int64             `json:"numeric_count"`
		Policy   map[string]string `json:"policy"`
	}
	if json.Unmarshal(m.Reference, &v) != nil || v.Status != "available" || v.Mapping != "verified" || v.RouteID != *m.RouteID || v.VisitID != *m.BoardingVisitID || v.Date != q.Date || v.Set != q.Set || v.Count <= 0 || v.Numeric <= 0 || v.Numeric > v.Count || v.Mean == nil || v.P90 == nil {
		return r
	}
	if math.IsNaN(*v.Mean) || math.IsInf(*v.Mean, 0) || math.IsNaN(*v.P90) || math.IsInf(*v.P90, 0) {
		return r
	}
	if q.RawTimeCode == "" && v.TimeCode != nil || q.RawTimeCode != "" && (v.TimeCode == nil || *v.TimeCode != q.RawTimeCode) {
		return r
	}
	r.Status = "available"
	r.Reason = "historical_reference_only_not_departure_time_prediction"
	r.Mean = v.Mean
	r.P90 = v.P90
	r.Observations = &v.Count
	r.NumericCount = &v.Numeric
	r.BoardingVisitID = m.BoardingVisitID
	r.AlightingVisitID = m.AlightingVisitID
	r.ReferenceDate = &v.Date
	r.ReferenceTimeCode = v.TimeCode
	r.Policy = v.Policy
	return r
}

// EvaluateCandidates leaves provider order untouched. Equal provider times get equal competition ranks.
// Rank is calculated over the returned (already mode-filtered) candidates only.
func EvaluateCandidates(out *Candidates) {
	if out.Policy == nil {
		out.Policy = map[string]string{}
	}
	out.Policy["recommendation"] = "provider_total_minutes_ascending_competition_rank_v1"
	out.Policy["recommendation_scope"] = "returned_candidates_only"
	out.Policy["comparison_metrics"] = "transfers_and_walking_are_separate_not_weighted"
	out.Policy["congestion_scoring"] = "not_used"
	order := make([]int, len(out.Paths))
	for i := range out.Paths {
		order[i] = i
		c := &out.Paths[i]
		e := Evaluation{Method: "travel_time_only_v1", TravelMinutes: c.TotalMinutes, Transfers: c.TransferCount, ContainsM5121: c.ContainsM5121, CongestionStatus: "unknown", References: []EvaluationReference{}}
		if c.ProviderTransfers != nil {
			e.Transfers = *c.ProviderTransfers
			e.TransferSource = "provider_reported"
		}
		available := 0
		for legIndex, leg := range c.Legs {
			if leg.Kind == "walk" {
				e.WalkingMinutes += leg.DurationMinutes
			}
			if leg.Kind == "bus" && len(leg.Options) == 0 || leg.Kind == "subway" && len(leg.ProviderData.Lanes) == 0 {
				e.References = append(e.References, EvaluationReference{LegIndex: legIndex, OptionIndex: -1, Type: leg.Kind, Status: "unknown", Reason: "missing_route_options"})
			}
			if leg.Kind == "bus" {
				for optionIndex, o := range leg.Options {
					r := optionReference(out.Query, legIndex, optionIndex, o)
					e.References = append(e.References, r)
					if r.Status == "available" {
						available++
					}
				}
			}
			if leg.Kind == "subway" {
				for optionIndex, lane := range leg.ProviderData.Lanes {
					e.References = append(e.References, EvaluationReference{LegIndex: legIndex, OptionIndex: optionIndex, Type: "subway", LineName: lane.Name, SubwayCode: lane.SubwayCode, Status: "unknown", Reason: "no_verified_boarding_statistics"})
				}
			}
		}
		if available > 0 {
			e.CongestionStatus = "available_reference"
			if available < len(e.References) {
				e.CongestionStatus = "partial_reference"
			}
		}
		c.Evaluation = e
	}
	sort.SliceStable(order, func(i, j int) bool { return out.Paths[order[i]].TotalMinutes < out.Paths[order[j]].TotalMinutes })
	rank := 0
	for i, index := range order {
		if i == 0 || out.Paths[index].TotalMinutes != out.Paths[order[i-1]].TotalMinutes {
			rank = i + 1
		}
		out.Paths[index].RecommendationRank = rank
	}
}
