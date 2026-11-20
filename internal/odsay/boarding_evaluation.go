package odsay

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"time"
)

// Weights are loaded once from deployment configuration, never from a public query.
// Scores are heuristic points, NOT failure probabilities or additional minutes.
type BoardingWeights struct {
	Travel      float64  `json:"travel_weight"`
	Risk        float64  `json:"risk_weight"`
	Mean        float64  `json:"mean_weight"`
	P90         float64  `json:"p90_weight"`
	History     float64  `json:"history_weight"`
	Seats       float64  `json:"seat_weight"`
	CGSTScale   float64  `json:"cgst_scale"`
	SeatScale   float64  `json:"seat_scale"`
	SampleScale float64  `json:"sample_scale"`
	Holidays    []string `json:"holidays"`
}

func (w BoardingWeights) Validate() error {
	for _, v := range []float64{w.Travel, w.Risk, w.Mean, w.P90, w.History, w.Seats, w.CGSTScale, w.SeatScale, w.SampleScale} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e6 {
			return errors.New("invalid scoring configuration")
		}
	}
	if w.Travel <= 0 || w.CGSTScale <= 0 || w.SeatScale <= 0 || w.SampleScale <= 0 || w.Mean+w.P90 <= 0 || w.History+w.Seats <= 0 {
		return errors.New("invalid scoring configuration")
	}
	for _, d := range w.Holidays {
		if _, e := time.Parse("2006-01-02", d); e != nil {
			return errors.New("invalid scoring calendar")
		}
	}
	return nil
}
func LoadBoardingWeights(path string) (BoardingWeights, error) {
	var w BoardingWeights
	b, e := os.ReadFile(path)
	if e != nil || json.Unmarshal(b, &w) != nil || w.Validate() != nil {
		return w, errors.New("boarding scoring configuration invalid")
	}
	return w, nil
}

type BoardingScore struct {
	Coverage        string          `json:"coverage"`
	Uncertainty     string          `json:"uncertainty"`
	ComparisonGroup string          `json:"comparison_group,omitempty"`
	GroupRank       *int            `json:"comparison_group_rank"`
	GroupSize       int             `json:"comparison_group_size"`
	ScoreComparable bool            `json:"score_comparable_across_candidates"`
	RankingPolicy   string          `json:"ranking_policy"`
	Score           float64         `json:"score"`
	TimeComponent   float64         `json:"time_component"`
	RiskComponent   float64         `json:"risk_component"`
	Risk            *float64        `json:"risk_index"`
	Status          string          `json:"risk_status"`
	LowSample       bool            `json:"low_sample"`
	BaselineRank    int             `json:"travel_time_rank"`
	RankChange      int             `json:"rank_change"`
	Reasons         []string        `json:"reasons"`
	Legs            []BoardingRisk  `json:"legs"`
	Weights         BoardingWeights `json:"weights"`
}
type BoardingRisk struct {
	Mean         *float64 `json:"mean_cgst"`
	P90          *float64 `json:"p90_cgst"`
	DayGroup     string   `json:"day_group,omitempty"`
	Leg          int      `json:"leg_index"`
	Status       string   `json:"status"`
	History      *float64 `json:"history_risk"`
	Seat         *float64 `json:"seat_risk"`
	Risk         *float64 `json:"risk_index"`
	Confidence   *float64 `json:"history_confidence"`
	Observations int64    `json:"observation_count"`
	LowSample    bool     `json:"low_sample"`
	SeatStatus   string   `json:"seat_status"`
	Reason       string   `json:"reason"`
}

func clamp(v float64) float64 { return math.Max(0, math.Min(1, v)) }
func finite(v *float64) bool  { return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 }
func dayGroup(date string, w BoardingWeights) string {
	for _, d := range w.Holidays {
		if date == d {
			return "holiday"
		}
	}
	d, e := time.Parse("2006-01-02", date)
	if e != nil {
		return "unknown"
	}
	if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		return "weekend"
	}
	return "weekday"
}
func optionRisk(index int, leg Leg, w BoardingWeights) BoardingRisk {
	r := BoardingRisk{Leg: index, Status: "unknown", SeatStatus: "unavailable", Reason: "no_matching_boarding_data"}
	// Lane alternatives are not separate required boardings. Do not pick a known
	// option as representative when alternatives have no measurements.
	if len(leg.Options) != 1 {
		r.Reason = "ambiguous_or_missing_route_options"
		return r
	}
	o := leg.Options[0]
	a := o.BoardingAnalysis
	m := o.Mapping
	if a == nil || a.Stop == nil || m == nil || m.RouteID == nil || m.BoardingVisitID == nil || m.AlightingVisitID == nil || m.Status == "visit_order_conflict" || m.BoardingStatus != "matched_declared_gbis_local_id" || m.AlightingStatus != "matched_declared_gbis_local_id" || a.Stop.RouteID != *m.RouteID || a.Stop.VisitID != *m.BoardingVisitID || a.Stop.GBISRoute != o.Lane.LocalID || leg.Boarding == nil || a.Stop.GBISStop != leg.Boarding.LocalID {
		return r
	}
	if a.History.Status == "available" && a.Time.Status == "matched_project_rule" && a.Time.TZON != "" {
		group := dayGroup(a.Time.ServiceDate, w)
		matches := 0
		for _, p := range a.History.Groups {
			if p.DayGroup != group || !finite(p.Mean) || !finite(p.P90) || p.NumericCount <= 0 || p.Observations < p.NumericCount {
				continue
			}
			matches++
			confidence := float64(p.NumericCount) / (float64(p.NumericCount) + w.SampleScale)
			risk := clamp((w.Mean*(*p.Mean) + w.P90*(*p.P90)) / (w.Mean + w.P90) / w.CGSTScale)
			r.Mean = p.Mean
			r.P90 = p.P90
			r.DayGroup = p.DayGroup
			r.History = &risk
			r.Confidence = &confidence
			r.Observations = p.Observations
			r.LowSample = p.LowSample || float64(p.NumericCount) < w.SampleScale
		}
		if matches != 1 {
			r.Mean = nil
			r.P90 = nil
			r.DayGroup = ""
			r.History = nil
			r.Confidence = nil
			r.Observations = 0
			r.LowSample = false
		}
	}
	r.SeatStatus = a.Realtime.Status
	if a.Realtime.Status == "available" {
		// Use the nearest Arrival slot only, never a later bus with more seats.
		first := -1
		for i, v := range a.Realtime.Vehicles {
			if v.ArrivalIndex > 0 && (first < 0 || v.ArrivalIndex < a.Realtime.Vehicles[first].ArrivalIndex) {
				first = i
			}
		}
		if first >= 0 {
			v := a.Realtime.Vehicles[first]
			if v.Status == "available" && v.VehicleID != nil && *v.VehicleID != "" && v.ETA != nil && *v.ETA >= 0 && v.Seats != nil && *v.Seats >= 0 {
				risk := 1 / (1 + float64(*v.Seats)/w.SeatScale)
				r.Seat = &risk
			}
		}
	}
	total, weight := 0.0, 0.0
	if r.History != nil && w.History > 0 {
		total += *r.History * w.History
		weight += w.History
	}
	if r.Seat != nil && w.Seats > 0 {
		total += *r.Seat * w.Seats
		weight += w.Seats
	}
	if weight > 0 {
		risk := total / weight
		r.Risk = &risk
		r.Status = "available"
		r.Reason = "heuristic_not_failure_probability"
	}
	return r
}
func EvaluateBoarding(out *Candidates, w BoardingWeights) {
	// Repeated evaluation must always compare against genuine travel-time ranks.
	for _, c := range out.Paths {
		if c.Evaluation.Method == "boarding_aware_v1" {
			EvaluateCandidates(out)
			break
		}
	}

	for i := range out.Paths {
		c := &out.Paths[i]
		b := &BoardingScore{TimeComponent: w.Travel * c.TotalMinutes, Status: "unknown", BaselineRank: c.RecommendationRank, Weights: w, Legs: []BoardingRisk{}, Reasons: []string{"heuristic_points_not_delay_or_probability", "unknown_is_not_zero_risk"}}
		known, transit := 0, 0
		for li, leg := range c.Legs {
			if leg.Kind == "walk" {
				continue
			}
			transit++
			r := optionRisk(li, leg, w)
			b.Legs = append(b.Legs, r)
			if r.Risk != nil {
				known++
				if b.Risk == nil || *r.Risk > *b.Risk {
					v := *r.Risk
					b.Risk = &v
				}
				b.LowSample = b.LowSample || (r.LowSample && r.History != nil && w.History > 0)
			}
		}
		if b.Risk != nil {
			b.Status = "available"
			if known < transit {
				b.Status = "partial"
			}
			b.RiskComponent = w.Risk * (*b.Risk)
			b.Reasons = append(b.Reasons, "bounded_max_observed_leg_risk")
		} else {
			b.Reasons = append(b.Reasons, "time_only_fallback_no_matching_risk_data")
		}
		if b.LowSample {
			b.Reasons = append(b.Reasons, "low_sample_is_uncertainty_not_risk_discount")
		}
		b.Coverage = "unknown"
		b.Uncertainty = "unknown_coverage"
		if known > 0 {
			b.Coverage = "partial"
			b.Uncertainty = "partial_coverage"
		}
		if known == transit && transit > 0 {
			b.Coverage = "full"
			b.Uncertainty = "no_low_sample_flag"
			if b.LowSample {
				b.Uncertainty = "low_sample"
			}
		}
		b.ComparisonGroup = comparisonGroup(c, b, w)
		b.Score = b.TimeComponent + b.RiskComponent
		c.Evaluation.Method = "boarding_aware_v1"
		c.Evaluation.Boarding = b
	}
	RankBoarding(out)
}
