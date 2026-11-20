package alternatives

import (
	"geoseulleong/internal/odsay"
	"geoseulleong/internal/reachability"
	"math"
)

type RouteStop struct {
	ProviderStationID string       `json:"provider_station_id,omitempty"`
	ProviderCode      *int         `json:"provider_code,omitempty"`
	Name              string       `json:"name"`
	GBISID            string       `json:"gbis_stop_id,omitempty"`
	VisitID           *int64       `json:"visit_id,omitempty"`
	Point             *odsay.Point `json:"point,omitempty"`
}
type RouteLeg struct {
	Phase           string    `json:"phase"`
	Mode            string    `json:"mode"`
	From            RouteStop `json:"from"`
	To              RouteStop `json:"to"`
	DurationMinutes *float64  `json:"duration_minutes"`
	RouteName       string    `json:"route_name,omitempty"`
	RouteID         string    `json:"route_id,omitempty"`
	Continuation    bool      `json:"same_vehicle_continuation"`
	Status          string    `json:"status"`
	ReasonCode      string    `json:"reason_code,omitempty"`
	DataOrigin      string    `json:"data_origin"`
}
type JourneyPhase struct {
	ProviderOverheadMinutes *float64   `json:"provider_overhead_minutes,omitempty"`
	Kind                    string     `json:"kind"`
	Status                  string     `json:"status"`
	DurationMinutes         *float64   `json:"duration_minutes"`
	KnownMinutes            float64    `json:"known_minutes"`
	ReasonCodes             []string   `json:"reason_codes"`
	Legs                    []RouteLeg `json:"legs"`
}
type FullJourney struct {
	RealtimeReasonCodes []string               `json:"realtime_reason_codes"`
	IncludesExpressWait bool                   `json:"includes_express_wait"`
	Origin              *odsay.Point           `json:"origin"`
	Destination         *odsay.Point           `json:"destination"`
	Status              string                 `json:"status"`
	ReasonCodes         []string               `json:"reason_codes"`
	Phases              []JourneyPhase         `json:"phases"`
	TotalMinutes        *float64               `json:"total_journey_minutes"`
	KnownMinutes        float64                `json:"known_minutes"`
	WalkingMinutes      *float64               `json:"walking_minutes"`
	Transfers           *int                   `json:"transfer_count"`
	TimeBasis           string                 `json:"time_basis"`
	ExpressWaitMinutes  *float64               `json:"express_wait_minutes"`
	Reachability        string                 `json:"reachability"`
	BoardingStatus      string                 `json:"boarding_status"`
	Vehicles            []reachability.Vehicle `json:"vehicles"`
}

func knownTime(v *float64) bool { return v != nil && *v >= 0 && !math.IsNaN(*v) && !math.IsInf(*v, 0) }
func visitStop(v Visit) RouteStop {
	id := v.ID
	return RouteStop{Name: v.Name, GBISID: v.GBIS, VisitID: &id, Point: v.Point}
}
func providerStop(name, id string, provider *int, x, y *float64) RouteStop {
	s := RouteStop{ProviderCode: provider, Name: name, Point: pointOf(x, y)}
	if provider != nil && *provider == 2 {
		s.GBISID = id
	}
	return s
}
func routeLeg(l odsay.SubPath, phase, origin string) RouteLeg {
	mode := "unknown"
	switch l.TrafficType {
	case 1:
		mode = "subway"
	case 2:
		mode = "bus"
	case 3:
		mode = "walk"
	}
	r := RouteLeg{Phase: phase, Mode: mode, From: providerStop(l.StartName, l.StartLocal, l.StartProvider, l.StartX, l.StartY), To: providerStop(l.EndName, l.EndLocal, l.EndProvider, l.EndX, l.EndY), DataOrigin: origin, Status: "known"}
	r.From.ProviderStationID = string(l.StartID)
	r.To.ProviderStationID = string(l.EndID)
	if knownTime(l.Minutes) {
		v := *l.Minutes
		r.DurationMinutes = &v
	} else {
		r.Status = "unknown"
		r.ReasonCode = "segment_time_missing"
	}
	if mode == "unknown" {
		r.Status = "unknown"
		r.ReasonCode = "segment_mode_unknown"
	}
	if len(l.Lanes) > 0 {
		r.RouteName = l.Lanes[0].BusNumber
		r.RouteID = l.Lanes[0].LocalID
		if r.RouteName == "" {
			r.RouteName = l.Lanes[0].Name
		}
	}
	return r
}
func phase(kind string, legs []RouteLeg) JourneyPhase {
	p := JourneyPhase{Kind: kind, Status: "known", ReasonCodes: []string{}, Legs: legs}
	valid := true
	for _, l := range legs {
		if !knownTime(l.DurationMinutes) || l.Status == "unknown" {
			valid = false
			reason := l.ReasonCode
			if reason == "" {
				reason = "segment_time_missing"
			}
			p.ReasonCodes = appendUnique(p.ReasonCodes, reason)
		}
		if knownTime(l.DurationMinutes) {
			p.KnownMinutes += *l.DurationMinutes
		}
	}
	if valid {
		sum := p.KnownMinutes
		p.DurationMinutes = &sum
		if len(legs) == 0 {
			p.Status = "not_required"
		}
	} else {
		p.Status = "unknown"
	}
	return p
}
func appendUnique(a []string, s string) []string {
	for _, x := range a {
		if x == s {
			return a
		}
	}
	return append(a, s)
}
func full(phases []JourneyPhase, r *reachability.Result) *FullJourney {
	j := &FullJourney{Status: "complete", ReasonCodes: []string{}, RealtimeReasonCodes: []string{}, Phases: phases, TimeBasis: "provider_segment_sum_excludes_unprovided_wait", Reachability: "unknown", BoardingStatus: "unknown", Vehicles: []reachability.Vehicle{}}
	sum, walk, transit := 0.0, 0.0, 0
	times, modes, walking := true, true, true
	for _, p := range phases {
		j.KnownMinutes += p.KnownMinutes
		if p.Kind == "access" && p.Status == "unknown" {
			walking = false
			modes = false
		}
		if p.ProviderOverheadMinutes != nil && *p.ProviderOverheadMinutes > 0 {
			walking = false
		}
		if p.DurationMinutes == nil || p.Status == "unknown" {
			times = false
			j.Status = "partial"
			for _, code := range p.ReasonCodes {
				j.ReasonCodes = appendUnique(j.ReasonCodes, code)
			}
		} else {
			sum += *p.DurationMinutes
		}
		for _, l := range p.Legs {
			if l.Mode == "unknown" {
				modes = false
				walking = false
			}
			if l.Mode == "walk" {
				if knownTime(l.DurationMinutes) {
					walk += *l.DurationMinutes
				} else {
					walking = false
				}
			}
			if (l.Mode == "bus" || l.Mode == "subway") && !l.Continuation {
				transit++
			}
		}
	}
	if times {
		j.TotalMinutes = &sum
	}
	if walking {
		j.WalkingMinutes = &walk
	}
	if modes {
		t := max(0, transit-1)
		j.Transfers = &t
	}
	if r == nil {
		j.RealtimeReasonCodes = []string{"realtime_unavailable"}
	}
	if r != nil {
		if r.Status != "reachable" {
			for _, code := range r.Reasons {
				j.RealtimeReasonCodes = appendUnique(j.RealtimeReasonCodes, code)
			}
			for _, vehicle := range r.Vehicles {
				if vehicle.Reason != "" {
					j.RealtimeReasonCodes = appendUnique(j.RealtimeReasonCodes, vehicle.Reason)
				}
			}
			if r.Status == "unreachable" && len(j.RealtimeReasonCodes) == 0 {
				j.RealtimeReasonCodes = append(j.RealtimeReasonCodes, "access_after_bus")
			}
		}
		j.Reachability = r.Status
		j.BoardingStatus = r.BoardingStatus
		j.Vehicles = r.Vehicles
		if j.Vehicles == nil {
			j.Vehicles = []reachability.Vehicle{}
		}
	}
	return j
}
func exactExpress(paths []odsay.Path, route odsay.Lane, start, end string) (*odsay.SubPath, bool) {
	var found *odsay.SubPath
	ambiguous := false
	for _, p := range paths {
		for _, l := range p.Legs {
			if l.Namespace == "kakao" || l.TrafficType != 2 || l.StartProvider == nil || *l.StartProvider != 2 || l.EndProvider == nil || *l.EndProvider != 2 || l.StartLocal != start || l.EndLocal != end || !knownTime(l.Minutes) {
				continue
			}
			same := false
			for _, lane := range l.Lanes {
				if (lane.Namespace == "" || lane.Namespace == "odsay") && lane.Provider != nil && *lane.Provider == 2 && lane.LocalID == route.LocalID && lane.BusNumber == route.BusNumber {
					same = true
				}
			}
			if !same {
				continue
			}
			if found != nil && *found.Minutes != *l.Minutes {
				ambiguous = true
			}
			copy := l
			found = &copy
		}
	}
	if ambiguous {
		return nil, true
	}
	return found, false
}

// ComposeJourneys reuses only exact provider sections. In particular, a 63-minute
// original bus leg is never assigned to a different boarding stop or prorated.
func ComposeJourneys(out *Result, p odsay.Path, legIndex int, paths []odsay.Path, route odsay.Lane, data Snapshot, referenceOrigin, origin, destination *odsay.Point, source string) {
	if source == "" {
		source = "odsay_recorded_response"
	}
	selected := p.Legs[legIndex]
	alight := providerStop(selected.EndName, selected.EndLocal, selected.EndProvider, selected.EndX, selected.EndY)
	if visit, ok := alightingVisit(out.Original, selected, data); ok {
		alight = visitStop(visit)
	}
	suffix := []RouteLeg{}
	for _, l := range p.Legs[legIndex+1:] {
		suffix = append(suffix, routeLeg(l, "onward", source))
	}
	if len(suffix) > 0 {
		if suffix[0].From.Name == "" {
			suffix[0].From = alight
		}
		last := &suffix[len(suffix)-1]
		if last.To.Name == "" {
			last.To.Name = "목적지"
		}
		if last.To.Point == nil {
			last.To.Point = destination
		}
	}
	onward := phase("onward", suffix)
	prefix := []RouteLeg{}
	for _, l := range p.Legs[:legIndex] {
		prefix = append(prefix, routeLeg(l, "access", source))
	}
	if len(prefix) > 0 {
		if prefix[0].From.Name == "" {
			prefix[0].From.Name = "출발지"
		}
		if prefix[0].From.Point == nil {
			prefix[0].From.Point = referenceOrigin
		}
		last := &prefix[len(prefix)-1]
		if last.To.Name == "" {
			last.To = visitStop(out.Original)
		}
	}
	originalBus := routeLeg(selected, "express", source)
	originalBus.From = visitStop(out.Original)
	originalBus.To = alight
	out.OriginalItinerary = full([]JourneyPhase{phase("access", prefix), phase("express", []RouteLeg{originalBus}), onward}, out.OriginalReachability)
	out.OriginalItinerary.Origin = referenceOrigin
	out.OriginalItinerary.Destination = destination
	comparable := origin == nil || referenceOrigin != nil && *origin == *referenceOrigin
	if !comparable {
		out.OriginalItinerary.Status = "reference_only"
		out.OriginalItinerary.ReasonCodes = appendUnique(out.OriginalItinerary.ReasonCodes, "original_origin_changed")
	}
	for i := range out.Candidates {
		c := &out.Candidates[i]
		from := visitStop(c.Visit)
		accessLegs := []RouteLeg{}
		accessPath := bestAccessPath(c.AccessPaths)
		if accessPath == nil {
			accessLegs = append(accessLegs, RouteLeg{Phase: "access", Mode: "unknown", From: RouteStop{Name: "출발지", Point: origin}, To: from, Status: "unknown", ReasonCode: "access_missing", DataOrigin: "unavailable"})
		} else {
			for _, l := range accessPath.Legs {
				accessLegs = append(accessLegs, routeLeg(l.Segment, "access", c.AccessPaths.DataOrigin))
			}
		}
		access := phase("access", accessLegs)
		if accessPath != nil {
			// Provider total can contain timing absent from section sums. Keep it once,
			// without adding it again as a leg or pretending that sections cover waiting.
			if access.Status != "unknown" {
				value := accessPath.Minutes
				if !knownTime(&value) || value+0.000001 < access.KnownMinutes {
					access.Status = "unknown"
					access.DurationMinutes = nil
					access.ReasonCodes = appendUnique(access.ReasonCodes, "access_time_conflict")
				} else {
					overhead := value - access.KnownMinutes
					if overhead > 0.000001 {
						access.ProviderOverheadMinutes = &overhead
					}
					access.DurationMinutes = &value
					access.KnownMinutes = value
				}
			}
			if accessPath.ArrivalStatus != "stop_id_and_coordinates_match" {
				access.Status = "unknown"
				access.DurationMinutes = nil
				access.ReasonCodes = appendUnique(access.ReasonCodes, "access_connection_unknown")
			}
			if len(access.Legs) > 0 {
				if access.Legs[0].From.Name == "" {
					access.Legs[0].From.Name = "출발지"
				}
				if access.Legs[0].From.Point == nil {
					access.Legs[0].From.Point = origin
				}
				access.Legs[len(access.Legs)-1].To = from
			}
		}
		bus := []RouteLeg{}
		var known *odsay.SubPath
		conflict := false
		busSource := source
		if c.ExpressSegment != nil {
			known = c.ExpressSegment.leg
			conflict = c.ExpressSegment.ReasonCode == "bus_time_conflict"
			if known != nil {
				busSource = c.ExpressSegment.DataOrigin
			}
		} else {
			known, conflict = exactExpress(paths, route, c.Visit.GBIS, selected.EndLocal)
		}
		visits := 0
		for _, v := range data.Visits {
			if v.GBIS == c.Visit.GBIS && direction(v) == direction(c.Visit) {
				visits++
			}
		}
		if visits > 1 {
			known = nil
			conflict = true
		}
		if known != nil {
			l := routeLeg(*known, "express", busSource)
			l.From = from
			l.To = alight
			bus = append(bus, l)
		} else {
			missing := RouteLeg{Phase: "express", Mode: "bus", From: from, To: alight, RouteName: route.BusNumber, RouteID: route.LocalID, Status: "unknown", ReasonCode: "bus_time_missing", DataOrigin: "unavailable"}
			if c.ExpressSegment != nil && c.ExpressSegment.ReasonCode != "" {
				missing.ReasonCode = c.ExpressSegment.ReasonCode
			}
			if conflict {
				missing.ReasonCode = "bus_time_conflict"
			}
			if c.Visit.Sequence < out.Original.Sequence && !conflict {
				missing.To = visitStop(out.Original)
				bus = append(bus, missing)
				continuation := originalBus
				continuation.Continuation = true
				bus = append(bus, continuation)
			} else {
				bus = append(bus, missing)
			}
		}
		c.Itinerary = full([]JourneyPhase{access, phase("express", bus), onward}, c.Reachability)
		c.Itinerary.Origin = origin
		c.Itinerary.Destination = destination
		if c.Status == "excluded" {
			c.Itinerary.TotalMinutes = nil
			c.Itinerary.Status = "partial"
			for _, reason := range c.Reasons {
				c.Itinerary.ReasonCodes = appendUnique(c.Itinerary.ReasonCodes, reason)
			}
		}
		if c.Comparison != nil {
			v := c.Comparison
			v.TotalMinutesDifference = nil
			v.TotalWalkingDifference = nil
			v.TotalTransferDifference = nil
			v.TotalJourneyMinutes = c.Itinerary.TotalMinutes
			v.TotalJourneyStatus = c.Itinerary.Status
			v.TotalWalkingMinutes = c.Itinerary.WalkingMinutes
			v.TotalTransfers = c.Itinerary.Transfers
			v.TotalTimeBasis = c.Itinerary.TimeBasis
			v.TotalComparisonStatus = "incomplete"
			if comparable && v.TotalJourneyMinutes != nil && out.OriginalItinerary.TotalMinutes != nil {
				v.TotalComparisonStatus = "comparable"
			}
			if !comparable {
				v.TotalComparisonStatus = "different_origin"
			}
			if comparable && v.TotalJourneyMinutes != nil && out.OriginalItinerary.TotalMinutes != nil {
				d := *v.TotalJourneyMinutes - *out.OriginalItinerary.TotalMinutes
				v.TotalMinutesDifference = &d
			}
			if comparable && v.TotalWalkingMinutes != nil && out.OriginalItinerary.WalkingMinutes != nil {
				d := *v.TotalWalkingMinutes - *out.OriginalItinerary.WalkingMinutes
				v.TotalWalkingDifference = &d
			}
			if comparable && v.TotalTransfers != nil && out.OriginalItinerary.Transfers != nil {
				d := *v.TotalTransfers - *out.OriginalItinerary.Transfers
				v.TotalTransferDifference = &d
			}
		}
		if c.RecommendationStatus == "unknown" {
			for _, reason := range c.Itinerary.ReasonCodes {
				c.RecommendationReasons = appendUnique(c.RecommendationReasons, reason)
			}
		}
	}
}
