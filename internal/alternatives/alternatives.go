// Package alternatives is an experimental, read-only boarding-stop comparator.
// Default comparisons make no provider calls; optional access uses an injected
// guarded searcher and never changes journey recommendations.
package alternatives

import (
	"context"
	"encoding/json"
	"errors"
	"geoseulleong/internal/odsay"
	"geoseulleong/internal/reachability"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

var ErrSearchExpired = errors.New("journey selection missing or expired")

var ErrInput = errors.New("invalid alternative boarding request")
var ErrIdentity = errors.New("official route/stop identity missing or ambiguous")

type Visit struct {
	ID             int64        `json:"visit_id"`
	Sequence       int          `json:"sequence"`
	TurnSequence   *int         `json:"turn_sequence"`
	GBIS           string       `json:"gbis_stop_id"`
	Name           string       `json:"original_name"`
	Via            bool         `json:"via_name"`
	Allowed        *bool        `json:"boarding_allowed"`
	Point          *odsay.Point `json:"point"`
	CRS            string       `json:"crs_status"`
	MappingCount   int          `json:"mapping_count"`
	STCIS          string       `json:"stcis_stop_id"`
	SourceSequence *int         `json:"stcis_sequence"`
}
type Observation struct {
	Date     string   `json:"date"`
	Stop     string   `json:"stop"`
	Sequence *int     `json:"sequence"`
	TZON     string   `json:"tzon"`
	CGST     *float64 `json:"cgst"`
}
type Snapshot struct {
	RouteID       int64         `json:"route_id"`
	GBIS          string        `json:"gbis_route_id"`
	STCIS         string        `json:"stcis_route_id"`
	MappingDate   string        `json:"mapping_reference_date"`
	Visits        []Visit       `json:"visits"`
	Rows          []Observation `json:"rows"`
	BaselineCount int           `json:"baseline_count"`
	FileCount     int           `json:"file_count"`
	Code50        int           `json:"file_code50_count"`
}
type Source interface {
	Load(context.Context, int64) (Snapshot, error)
}
type Identity interface {
	OriginalBoarding(context.Context, odsay.Lane, odsay.SubPath) (int64, int64, error)
}
type Request struct {
	SearchID            string       `json:"search_id,omitempty"`
	Origin              *odsay.Point `json:"origin,omitempty"`
	Recommend           bool         `json:"recommend,omitempty"`
	IncludeReachability bool         `json:"include_reachability,omitempty"`
	IncludeAccess       bool         `json:"include_access,omitempty"`
	PathIndex           int          `json:"path_index"`
	LegIndex            int          `json:"leg_index"`
	Date                string       `json:"date"`
	TZON                string       `json:"tzon"`
	Window              int          `json:"sequence_window"`
}
type Profile struct {
	Status    string   `json:"status"`
	Mean      *float64 `json:"mean_cgst"`
	P90       *float64 `json:"p90_cgst"`
	Count     int      `json:"observation_count"`
	Numeric   int      `json:"numeric_count"`
	LowSample bool     `json:"low_sample"`
}
type Candidate struct {
	ExpressSegment        *ExpressSegment      `json:"express_segment,omitempty"`
	Itinerary             *FullJourney         `json:"itinerary,omitempty"`
	Comparison            *Comparison          `json:"comparison,omitempty"`
	RecommendationStatus  string               `json:"recommendation_status,omitempty"`
	RecommendationReasons []string             `json:"recommendation_reasons,omitempty"`
	RecommendationRank    *int                 `json:"recommendation_rank,omitempty"`
	Reachability          *reachability.Result `json:"realtime_feasibility,omitempty"`
	AccessPaths           *AccessResult        `json:"access_paths,omitempty"`
	Visit                 Visit                `json:"visit"`
	Relation              string               `json:"relation"`
	SequenceDelta         int                  `json:"sequence_delta"`
	Metres                *float64             `json:"straight_line_metres"`
	Profile               Profile              `json:"profile"`
	MeanDifference        *float64             `json:"mean_difference"`
	P90Difference         *float64             `json:"p90_difference"`
	Status                string               `json:"status"`
	Reasons               []string             `json:"review_reasons"`
	Confirmed             bool                 `json:"confirmed_recommendation"`
	Access                map[string]any       `json:"access_search"`
}
type Result struct {
	OriginalItinerary    *FullJourney         `json:"original_itinerary,omitempty"`
	DataOrigin           string               `json:"data_origin,omitempty"`
	OriginalJourney      *JourneySummary      `json:"original_journey,omitempty"`
	OriginalAccess       *AccessResult        `json:"original_access,omitempty"`
	OriginalReachability *reachability.Result `json:"original_realtime_feasibility,omitempty"`
	Request              Request              `json:"request"`
	Route                int64                `json:"route_id"`
	Original             Visit                `json:"original"`
	Profile              Profile              `json:"original_profile"`
	Candidates           []Candidate          `json:"candidates"`
	Examined             []Candidate          `json:"examined"`
	Policy               map[string]any       `json:"policy"`
	Baseline             int                  `json:"baseline_observations"`
	Additional           int                  `json:"file_observations"`
}
type Service struct {
	SavedSegments *AccessReplay
	Destination   *odsay.Point
	Selections    *odsay.Selections
	DataOrigin    string
	ReachProvider reachability.Provider
	Clock         func() time.Time
	RecordedAt    *time.Time
	Origin        *odsay.Point
	AccessPlanner *AccessPlanner
	Source        Source
	Identity      Identity
	Recorded      odsay.Response
}

func (s *Service) Search(ctx context.Context, q Request) (Result, error) {
	if !Valid(q) {
		return Result{}, ErrInput
	}
	selected := *s
	if q.SearchID != "" {
		snapshot, ok, err := s.Selections.GetContext(ctx, q.SearchID)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			return Result{}, ErrSearchExpired
		}
		if q.PathIndex >= len(snapshot.Response.Paths) || !odsay.PathAllowed(snapshot.Query.Mode, snapshot.Response.Paths[q.PathIndex].Type) {
			return Result{}, ErrInput
		}
		selected.Recorded = snapshot.Response
		selected.Origin = &snapshot.Query.Start
		selected.Destination = &snapshot.Query.End
		selected.RecordedAt = &snapshot.CapturedAt
		selected.DataOrigin = snapshot.Origin
	} else if len(s.Recorded.Paths) == 0 {
		return Result{}, ErrSearchExpired
	}
	return selected.searchSelected(ctx, q)
}
func (s *Service) searchSelected(ctx context.Context, q Request) (Result, error) {
	if q.PathIndex >= len(s.Recorded.Paths) {
		return Result{}, ErrInput
	}
	p := s.Recorded.Paths[q.PathIndex]
	if q.LegIndex >= len(p.Legs) {
		return Result{}, ErrInput
	}
	leg := p.Legs[q.LegIndex]
	if leg.TrafficType != 2 {
		return Result{}, ErrInput
	}
	// Alternative route lanes may be unrelated lines: require one declared eligible route.
	lanes := []odsay.Lane{}
	for _, l := range leg.Lanes {
		if l.Type != nil && (*l.Type == 4 || *l.Type == 14) || l.BusNumber == "M5121" {
			lanes = append(lanes, l)
		}
	}
	if len(lanes) != 1 {
		return Result{}, ErrIdentity
	}
	route, visit, e := s.Identity.OriginalBoarding(ctx, lanes[0], leg)
	if e != nil {
		return Result{}, e
	}
	data, e := s.Source.Load(ctx, route)
	if e != nil {
		return Result{}, e
	}
	if data.RouteID != route || data.GBIS != lanes[0].LocalID {
		return Result{}, ErrIdentity
	}
	out, e := Compare(q, visit, data)
	if e != nil {
		return out, e
	}
	if q.Recommend {
		validateCandidateDestination(&out, leg, data)
	}
	origin := s.Origin
	if q.Origin != nil {
		origin = q.Origin
	}
	if q.Recommend {
		q.IncludeAccess = true
		q.IncludeReachability = true
	}
	if q.IncludeAccess || q.IncludeReachability {
		EnrichAccess(ctx, &out, origin, lanes[0], s.AccessPlanner)
		if q.Origin != nil {
			for i := range out.Candidates {
				if out.Candidates[i].AccessPaths != nil {
					out.Candidates[i].AccessPaths.OriginSource = "request_coordinates"
				}
			}
		}
	}
	if q.Recommend {
		EnrichExpress(ctx, &out, leg, s.Recorded.Paths, lanes[0], data, s.AccessPlanner, s.Selections, s.DataOrigin, s.SavedSegments.Snapshots())
	}
	if q.IncludeReachability {
		prefix := p.Legs[:q.LegIndex]
		if q.Origin != nil && (s.Origin == nil || *q.Origin != *s.Origin) {
			prefix = nil
		}
		EnrichFeasibility(ctx, &out, data, origin, lanes[0], leg, prefix, s.RecordedAt, s.AccessPlanner, s.ReachProvider, s.Clock)
	}
	if q.Recommend {
		out.DataOrigin = s.DataOrigin
		Summarize(&out, p)
		out.Policy["max_odsay_segment_queries"] = min(3, len(out.Candidates))
		out.Policy["max_odsay_total_queries"] = 2 * min(3, len(out.Candidates))
		ComposeJourneys(&out, p, q.LegIndex, s.Recorded.Paths, lanes[0], data, s.Origin, origin, s.Destination, s.DataOrigin)
	}
	if s.DataOrigin != "" {
		out.DataOrigin = s.DataOrigin
		if out.OriginalJourney != nil {
			out.OriginalJourney.DataOrigin = s.DataOrigin
			out.OriginalJourney.ReferenceOnly = strings.Contains(s.DataOrigin, "recorded") || (q.Origin != nil && (s.Origin == nil || *q.Origin != *s.Origin))
		}
		if out.OriginalAccess != nil {
			out.OriginalAccess.DataOrigin = s.DataOrigin + "_preboarding_sections"
			out.OriginalAccess.OriginSource = "search_request_coordinates"
		}
	}
	return out, nil
}
func Valid(q Request) bool {
	d, e := time.Parse("2006-01-02", q.Date)
	return (q.SearchID == "" || regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(q.SearchID)) && (q.Origin == nil || validPoint(q.Origin)) && e == nil && d.Format("2006-01-02") == q.Date && regexp.MustCompile(`^[0-9]{2}$`).MatchString(q.TZON) && q.PathIndex >= 0 && q.LegIndex >= 0 && q.Window >= 1 && q.Window <= 20
}
func profile(q Request, v Visit, s Snapshot) Profile {
	p := Profile{Status: "unavailable", LowSample: true}
	if v.MappingCount != 1 || v.STCIS == "" || v.SourceSequence == nil {
		p.Status = "mapping_unverified"
		return p
	}
	vals := []float64{}
	for _, r := range s.Rows {
		if r.Date == q.Date && r.TZON == q.TZON && r.Stop == v.STCIS && r.Sequence != nil && *r.Sequence == *v.SourceSequence {
			p.Count++
			if r.CGST != nil && !math.IsNaN(*r.CGST) && !math.IsInf(*r.CGST, 0) {
				vals = append(vals, *r.CGST)
			}
		}
	}
	p.Numeric = len(vals)
	p.LowSample = p.Numeric < 30
	if len(vals) == 0 {
		return p
	}
	sort.Float64s(vals)
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	m := sum / float64(len(vals))
	pos := 0.9 * float64(len(vals)-1)
	lo := int(pos)
	hi := int(math.Ceil(pos))
	quantile := vals[lo] + (vals[hi]-vals[lo])*(pos-float64(lo))
	p.Mean = &m
	p.P90 = &quantile
	p.Status = "available"
	return p
}
func direction(v Visit) string {
	if v.TurnSequence == nil || *v.TurnSequence <= 0 {
		return "unknown"
	}
	if v.Sequence == *v.TurnSequence {
		return "turn"
	}
	if v.Sequence < *v.TurnSequence {
		return "outbound"
	}
	return "return"
}
func Compare(q Request, original int64, s Snapshot) (Result, error) {
	out := Result{Request: q, Route: s.RouteID, Candidates: []Candidate{}, Examined: []Candidate{}, Baseline: s.BaselineCount, Additional: s.FileCount, Policy: map[string]any{"method": "lower_mean_same_date_raw_tzon_experimental_v1", "max_candidates": 3, "external_api_calls": 0, "future_access_request_budget": 3, "access_routes_not_requested": true, "cgst_definition": "hypothesis_unverified", "tzon_mapping": "raw_code_only_no_clock_conversion", "historical_mapping": "baseline_pairs_not_verified_for_other_dates", "distance": "straight_line_not_walk_route; CRS_hypothesis", "failure_probability": "not_computed", "duplicates": "preserved", "mapping_reference_date": s.MappingDate}}
	if !Valid(q) {
		return out, ErrInput
	}
	seenIDs, seenSequences := map[int64]bool{}, map[int]bool{}
	for _, v := range s.Visits {
		if v.ID <= 0 || v.Sequence <= 0 || seenIDs[v.ID] || seenSequences[v.Sequence] {
			return out, ErrIdentity
		}
		seenIDs[v.ID] = true
		seenSequences[v.Sequence] = true
	}
	found := 0
	for _, v := range s.Visits {
		if v.ID == original {
			out.Original = v
			found++
		}
	}
	if found != 1 {
		return out, ErrIdentity
	}
	out.Profile = profile(q, out.Original, s)
	for _, v := range s.Visits {
		delta := v.Sequence - out.Original.Sequence
		if delta == 0 || delta > q.Window || delta < -q.Window {
			continue
		}
		c := Candidate{Visit: v, SequenceDelta: delta, Relation: "after", Profile: profile(q, v, s), Status: "not_comparable", Reasons: []string{}, Access: map[string]any{"status": "not_requested", "max_calls": 1, "live_budget_required": true, "provider": "odsay", "destination": v.Point, "official_gbis_stop_id": v.GBIS}}
		if delta < 0 {
			c.Relation = "before"
		}
		if out.Original.Point != nil && v.Point != nil {
			d := straightLine(*out.Original.Point, *v.Point)
			c.Metres = &d
		}
		if direction(out.Original) == "unknown" || direction(v) == "unknown" || direction(v) != direction(out.Original) || v.TurnSequence == nil || out.Original.TurnSequence == nil || *v.TurnSequence != *out.Original.TurnSequence {
			c.Status = "excluded"
			c.Reasons = append(c.Reasons, "direction_or_turning_point_conflict")
		}
		if v.Allowed != nil && !*v.Allowed {
			c.Status = "excluded"
			c.Reasons = append(c.Reasons, "boarding_not_allowed")
		}
		if (v.Via || strings.Contains(v.Name, "(경유)") || strings.Contains(v.Name, "(미정차)")) && (v.Allowed == nil || !*v.Allowed) {
			c.Status = "excluded"
			c.Reasons = append(c.Reasons, "via_boarding_unverified")
		}
		if v.Allowed == nil {
			c.Reasons = append(c.Reasons, "boarding_permission_unverified")
		}
		if v.MappingCount != 1 {
			c.Reasons = append(c.Reasons, "stop_mapping_unverified_or_ambiguous")
		}
		if q.Date != s.MappingDate {
			c.Reasons = append(c.Reasons, "historical_mapping_unverified")
		}
		if v.Point == nil {
			c.Access["status"] = "coordinates_unavailable"
			c.Access["max_calls"] = 0
		}
		if c.Metres == nil {
			c.Reasons = append(c.Reasons, "distance_unavailable")
		}
		if c.Profile.LowSample || out.Profile.LowSample {
			c.Reasons = append(c.Reasons, "low_sample")
		}
		if c.Status != "excluded" && c.Profile.Mean != nil && out.Profile.Mean != nil {
			d := *c.Profile.Mean - *out.Profile.Mean
			c.MeanDifference = &d
			p := *c.Profile.P90 - *out.Profile.P90
			c.P90Difference = &p
			c.Status = "not_lower_mean"
			if p > 0 {
				c.Reasons = append(c.Reasons, "p90_higher_than_original")
			}
			if d < 0 {
				c.Status = "experimental_lower_mean"
				out.Candidates = append(out.Candidates, c)
			}
		}
		out.Examined = append(out.Examined, c)
	}
	sort.SliceStable(out.Candidates, func(i, j int) bool {
		a, b := out.Candidates[i], out.Candidates[j]
		if *a.Profile.Mean != *b.Profile.Mean {
			return *a.Profile.Mean < *b.Profile.Mean
		}
		if *a.Profile.P90 != *b.Profile.P90 {
			return *a.Profile.P90 < *b.Profile.P90
		}
		if math.Abs(float64(a.SequenceDelta)) != math.Abs(float64(b.SequenceDelta)) {
			return math.Abs(float64(a.SequenceDelta)) < math.Abs(float64(b.SequenceDelta))
		}
		return a.Visit.Sequence < b.Visit.Sequence
	})
	if len(out.Candidates) > 3 {
		out.Candidates = out.Candidates[:3]
	}
	return out, nil
}
func (s Snapshot) JSON() ([]byte, error) { return json.Marshal(s) }

// Straight-line Haversine metres only; this is not a pedestrian access route.
func straightLine(a, b odsay.Point) float64 {
	rad := math.Pi / 180
	dy := (b.Lat - a.Lat) * rad
	dx := (b.Lon - a.Lon) * rad
	h := math.Sin(dy/2)*math.Sin(dy/2) + math.Cos(a.Lat*rad)*math.Cos(b.Lat*rad)*math.Sin(dx/2)*math.Sin(dx/2)
	return 6371008.8 * 2 * math.Asin(math.Sqrt(math.Min(1, math.Max(0, h))))
}
