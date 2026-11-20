// Package reachability compares access estimates with matched reported vehicle ETAs.
// It never predicts ETA from position or turns seats/CGST into a success probability.
package reachability

import (
	"geoseulleong/internal/gbis"
	"math"
	"time"
)

type Coverage struct {
	Walking      *bool      `json:"walking_included"`
	TransferWait *bool      `json:"transfer_wait_included"`
	Connections  *bool      `json:"connections_included"`
	Evidence     string     `json:"evidence"`
	CapturedAt   *time.Time `json:"captured_at,omitempty"`
}
type Access struct {
	Seconds              *float64 `json:"seconds"`
	DestinationValidated bool     `json:"destination_validated"`
	Coverage             Coverage `json:"coverage"`
}
type Stop struct {
	Route           string `json:"route_id"`
	ID              string `json:"station_id"`
	Order           int    `json:"order"`
	Turn            int    `json:"turn_order"`
	BoardingAllowed *bool  `json:"boarding_allowed"`
}
type ArrivalSnapshot struct {
	Arrival    gbis.Arrival `json:"arrival"`
	ReceivedAt time.Time    `json:"received_at"`
	Origin     string       `json:"data_origin"`
}
type LocationsSnapshot struct {
	Route      string              `json:"route_id"`
	Locations  gbis.LocationResult `json:"locations"`
	ReceivedAt time.Time           `json:"received_at"`
	Origin     string              `json:"data_origin"`
}
type Vehicle struct {
	ID              string   `json:"vehicle_id"`
	Position        *int     `json:"station_order"`
	Status          string   `json:"status"`
	Reason          string   `json:"reason,omitempty"`
	ETASeconds      *float64 `json:"remaining_eta_seconds"`
	ReportedMinutes *int     `json:"reported_eta_minutes"`
	ReportedSeconds *int     `json:"reported_eta_seconds"`
	ArrivalSeats    *int     `json:"arrival_reported_seats"`
	LocationSeats   *int     `json:"location_reported_seats"`
	SeatStatus      string   `json:"seat_status"`
}
type Result struct {
	Status            string     `json:"reachability"`
	BoardingStatus    string     `json:"boarding_status"`
	Reasons           []string   `json:"reasons"`
	Access            Access     `json:"access"`
	Vehicles          []Vehicle  `json:"vehicles"`
	AsOf              time.Time  `json:"evaluated_at"`
	ArrivalAt         *time.Time `json:"arrival_received_at"`
	LocationsAt       *time.Time `json:"locations_received_at"`
	ProviderUpdatedAt *time.Time `json:"provider_updated_at"`
	Meaning           string     `json:"meaning"`
	DataOrigin        string     `json:"data_origin"`
}

func Unknown(a Access, now time.Time, reason string) Result {
	return Result{Status: "unknown", BoardingStatus: "unknown", Reasons: []string{reason}, Access: a, Vehicles: []Vehicle{}, AsOf: now, Meaning: "reported_vehicles_if_depart_now_not_boarding_success_or_future_service_guarantee"}
}
func fresh(at, now time.Time) bool {
	return !at.IsZero() && !at.After(now.Add(5*time.Second)) && now.Sub(at) <= 60*time.Second
}
func accessComplete(a Access, now time.Time) bool {
	return a.Seconds != nil && *a.Seconds >= 0 && !math.IsNaN(*a.Seconds) && !math.IsInf(*a.Seconds, 0) && a.DestinationValidated && a.Coverage.Walking != nil && *a.Coverage.Walking && a.Coverage.TransferWait != nil && *a.Coverage.TransferWait && a.Coverage.Connections != nil && *a.Coverage.Connections && a.Coverage.Evidence != "" && a.Coverage.CapturedAt != nil && fresh(*a.Coverage.CapturedAt, now)
}
func eta(v gbis.Vehicle) *float64 {
	var value float64
	if v.Seconds != nil && *v.Seconds >= 0 {
		value = float64(*v.Seconds)
	} else if v.Minutes != nil && *v.Minutes >= 0 {
		value = float64(*v.Minutes) * 60
	} else {
		return nil
	}
	return &value
}
func nonnegative(v *int) *int {
	if v == nil || *v < 0 {
		return nil
	}
	return v
}
func Evaluate(stop Stop, routeVisits map[int]string, access Access, a ArrivalSnapshot, l LocationsSnapshot, now time.Time) Result {
	out := Unknown(access, now, "no_matched_vehicle")
	out.Reasons = []string{}
	out.ArrivalAt = &a.ReceivedAt
	out.LocationsAt = &l.ReceivedAt
	out.DataOrigin = a.Origin + "+" + l.Origin
	if stop.BoardingAllowed != nil && !*stop.BoardingAllowed {
		out.BoardingStatus = "not_allowed"
	} else if stop.BoardingAllowed == nil {
		out.Reasons = append(out.Reasons, "boarding_permission_unverified")
	}
	if stop.Route == "" || stop.ID == "" || stop.Turn <= 0 || stop.Order <= 0 || stop.Order == stop.Turn || routeVisits[stop.Order] != stop.ID {
		out.Reasons = append(out.Reasons, "stop_direction_or_visit_unverified")
		return out
	}
	if a.Arrival.RouteID != stop.Route || a.Arrival.StationID != stop.ID || a.Arrival.Order != stop.Order || l.Route != stop.Route {
		out.Reasons = append(out.Reasons, "response_identity_conflict")
		return out
	}
	if !fresh(a.ReceivedAt, now) || !fresh(l.ReceivedAt, now) {
		out.Reasons = append(out.Reasons, "stale_or_future_gbis_snapshot")
		return out
	}
	if math.Abs(a.ReceivedAt.Sub(l.ReceivedAt).Seconds()) > 5 {
		out.Reasons = append(out.Reasons, "arrival_location_time_skew")
		return out
	}
	if a.Arrival.Flag != "PASS" && a.Arrival.Flag != "RUN" {
		out.Reasons = append(out.Reasons, "arrival_operation_unverified")
		return out
	}
	complete := accessComplete(access, now)
	if !complete {
		out.Reasons = append(out.Reasons, "access_time_incomplete_or_stale")
	}
	seen := map[string]bool{}
	for _, v := range a.Arrival.Vehicles {
		if v.VehicleID != nil && *v.VehicleID != "" {
			if seen[*v.VehicleID] {
				out.Reasons = append(out.Reasons, "duplicate_arrival_vehicle")
				return out
			}
			seen[*v.VehicleID] = true
		}
	}
	reachable, unreachable, unknown := 0, 0, 0
	for _, v := range a.Arrival.Vehicles {
		if v.VehicleID == nil || *v.VehicleID == "" {
			continue
		}
		item := Vehicle{ID: *v.VehicleID, Status: "unknown", SeatStatus: "unavailable", ReportedMinutes: v.Minutes, ReportedSeconds: v.Seconds}
		matches := []gbis.Location{}
		for _, loc := range l.Locations.Vehicles {
			if loc.VehicleID == item.ID {
				matches = append(matches, loc)
			}
		}
		if len(matches) != 1 {
			item.Reason = "location_missing_or_ambiguous"
			unknown++
			out.Vehicles = append(out.Vehicles, item)
			continue
		}
		loc := matches[0]
		item.Position = loc.StationOrder
		if loc.RouteID != stop.Route || loc.StationOrder == nil || loc.StationID == nil || routeVisits[*loc.StationOrder] != *loc.StationID {
			item.Reason = "vehicle_route_or_position_identity_conflict"
			unknown++
			out.Vehicles = append(out.Vehicles, item)
			continue
		}
		item.ArrivalSeats = nonnegative(v.Seats)
		item.LocationSeats = nonnegative(loc.RemainingSeats)
		if item.ArrivalSeats != nil || item.LocationSeats != nil {
			item.SeatStatus = "reported"
		}
		if item.ArrivalSeats != nil && item.LocationSeats != nil && *item.ArrivalSeats != *item.LocationSeats {
			item.SeatStatus = "conflict_preserved"
		}
		pos := *loc.StationOrder
		if pos == stop.Turn || stop.Order < stop.Turn && pos > stop.Turn || stop.Order > stop.Turn && pos < stop.Turn {
			item.Reason = "direction_conflict_or_turning_point"
			unknown++
			out.Vehicles = append(out.Vehicles, item)
			continue
		}
		if pos > stop.Order {
			item.Status = "excluded"
			item.Reason = "already_passed_candidate"
			out.Vehicles = append(out.Vehicles, item)
			continue
		}
		// Being at the target without a departure state is not proof of a future arrival.
		if pos == stop.Order {
			item.Reason = "at_target_position_ambiguous"
			unknown++
			out.Vehicles = append(out.Vehicles, item)
			continue
		}
		seconds := eta(v)
		if seconds == nil {
			item.Reason = "eta_unavailable_or_conflicting"
			unknown++
			out.Vehicles = append(out.Vehicles, item)
			continue
		}
		remaining := *seconds - math.Max(0, now.Sub(a.ReceivedAt).Seconds())
		if remaining < 0 {
			item.Reason = "eta_elapsed"
			unknown++
			out.Vehicles = append(out.Vehicles, item)
			continue
		}
		item.ETASeconds = &remaining

		if !complete {
			item.Reason = "access_time_incomplete_or_stale"
			unknown++
		} else if (a.Origin == "synthetic_fixture" || l.Origin == "synthetic_fixture") && access.Coverage.Evidence != "synthetic_fixture" {
			item.Reason = "mixed_synthetic_and_recorded_evidence"
			unknown++
		} else if access.Coverage.Evidence == "synthetic_fixture" && (a.Origin != "synthetic_fixture" || l.Origin != "synthetic_fixture") {
			item.Reason = "mixed_synthetic_and_recorded_evidence"
			unknown++
		} else if *access.Seconds <= remaining {
			item.Status = "reachable"
			reachable++
		} else {
			item.Status = "unreachable"
			unreachable++
		}
		out.Vehicles = append(out.Vehicles, item)
	}
	switch {
	case reachable > 0:
		out.Status = "reachable"
	case unknown > 0:
		out.Status = "unknown"
	case unreachable > 0:
		out.Status = "unreachable"
	default:
		out.Reasons = append(out.Reasons, "no_eligible_matched_arrival_vehicle")
	}
	return out
}

// ReportedETASeconds normalizes only supplied fields; it never predicts ETA.
func ReportedETASeconds(v gbis.Vehicle) *float64 { return eta(v) }
