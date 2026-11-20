package gbis

import (
	"context"
	"errors"
	"net/url"
	"sort"
)

// Location is one vehicle reported by the route-wide position feed, not an ETA.
type Location struct {
	RouteTypeCode  *int     `json:"route_type_code"`
	VehicleID      string   `json:"vehicle_id"`
	PlateNo        *string  `json:"plate_no"`
	RouteID        string   `json:"route_id"`
	StationID      *string  `json:"station_id"`
	StationOrder   *int     `json:"station_order"`
	StateCode      *int     `json:"state_code"`
	RemainingSeats *int     `json:"remaining_seats"`
	SeatStatus     string   `json:"seat_status"`
	ObservedFields []string `json:"observed_fields"`
}
type LocationResult struct {
	Status   string     `json:"status"`
	Vehicles []Location `json:"vehicles"`
}

func (c *Client) Locations(ctx context.Context, routeID string) (LocationResult, error) {
	out := LocationResult{Status: "available", Vehicles: []Location{}}
	if !id(routeID) {
		return out, &Error{Kind: "invalid_route_id"}
	}
	rows, e := c.get(ctx, "/buslocationservice/v2/getBusLocationListv2", url.Values{"routeId": {routeID}}, "busLocationList")
	return locationRows(rows, e, routeID)
}

func locationRows(rows []node, e error, routeID string) (LocationResult, error) {
	out := LocationResult{Status: "available", Vehicles: []Location{}}
	if e != nil {
		var api *Error
		if errors.As(e, &api) {
			if api.Kind == "no_data" {
				out.Status = "empty"
				return out, nil
			}
			if api.Kind == "api" && api.APICode == "4" {
				out.Status = "api_no_result"
				return out, nil
			}
		}
		return out, e
	}
	seen := map[string]bool{}
	for _, n := range rows {
		if n.val("routeId") != routeID || !id(n.val("vehId")) || seen[n.val("vehId")] {
			return out, &Error{Kind: "invalid_location_identity"}
		}
		seen[n.val("vehId")] = true
		v := Location{VehicleID: n.val("vehId"), RouteID: routeID, SeatStatus: "unknown", ObservedFields: n.fields()}
		if x := n.val("plateNo"); x != "" && x != "-1" {
			v.PlateNo = &x
		}
		if x := n.val("stationId"); id(x) {
			v.StationID = &x
		} else if x != "" && x != "-1" && x != "0" {
			return out, &Error{Kind: "invalid_location_station_id"}
		}
		for _, f := range []struct {
			name string
			dest **int
		}{{"stationSeq", &v.StationOrder}, {"stateCd", &v.StateCode}, {"remainSeatCnt", &v.RemainingSeats}, {"routeTypeCd", &v.RouteTypeCode}} {
			x, e := optionalInt(n, f.name)
			if e != nil {
				return out, e
			}
			*f.dest = x
		}
		if v.StationOrder != nil && *v.StationOrder == 0 {
			v.StationOrder = nil
		}
		if v.RouteTypeCode != nil && *v.RouteTypeCode == 0 {
			v.RouteTypeCode = nil
		}
		if v.StateCode != nil && *v.StateCode > 2 {
			return out, &Error{Kind: "invalid_location_state"}
		}
		if v.RemainingSeats != nil {
			v.SeatStatus = "provided"
		}
		out.Vehicles = append(out.Vehicles, v)
	}
	sort.Slice(out.Vehicles, func(i, j int) bool { return out.Vehicles[i].VehicleID < out.Vehicles[j].VehicleID })
	return out, nil
}

// Position compares route sequence numbers; it does not predict future trips.
func (v Location) Position(target, turn int) string {
	if target <= 0 || turn <= target || v.StationOrder == nil {
		return "unknown"
	}
	switch seq := *v.StationOrder; {
	case seq < target:
		return "before_target"
	case seq == target:
		return "at_target"
	case seq < turn:
		return "after_target_outbound"
	case seq == turn:
		return "turning_point"
	default:
		return "return_leg"
	}
}
func (a Arrival) OperationState() string {
	switch a.Flag {
	case "RUN", "PASS":
		return "운행 중"
	case "STOP":
		return "운행 종료"
	case "WAIT":
		return "회차지 대기"
	default:
		return "정보 없음"
	}
}

// ParseLocationsXML reuses identity/seat normalization without any network.
func ParseLocationsXML(raw []byte, status int, route string) (LocationResult, error) {
	if len(raw) > maxBody || !id(route) {
		return LocationResult{}, &Error{Kind: "invalid_recorded_locations"}
	}
	rows, e := parse(raw, status, "busLocationList")
	return locationRows(rows, e, route)
}
