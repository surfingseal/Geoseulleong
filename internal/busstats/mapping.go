package busstats

import "geoseulleong/internal/gbis"

// MappingEvidence must be supplied from a checked official ID specification.
// A matching number/name alone never establishes a common ID namespace.
type MappingEvidence struct {
	StationNamespaceConfirmed bool
	OfficialReference         string
}
type Match struct {
	RouteID           *string `json:"routeId"`
	StationID         *string `json:"gbisStationId"`
	StationOrders     []int   `json:"stationOrders"`
	Status            string  `json:"status"`
	DirectionVerified bool    `json:"directionVerified"`
	ReviewRequired    bool    `json:"reviewRequired"`
}

// MatchM5121 uses the previously verified unique GBIS route, but does not
// convert a statistics direction label into a GBIS direction without evidence.
func MatchM5121(r Record, route gbis.Route, stations []gbis.Station, evidence MappingEvidence) Match {
	out := Match{Status: "route_unverified", ReviewRequired: true, StationOrders: []int{}}
	if r.RouteNumber == nil || *r.RouteNumber != "M5121" || route.Name != "M5121" || route.ID != "234001317" {
		return out
	}
	out.RouteID = &route.ID
	if r.StationID == nil {
		out.Status = "station_id_missing"
		return out
	}
	matches := []gbis.Station{}
	for _, s := range stations {
		if s.ID == *r.StationID {
			matches = append(matches, s)
		}
	}
	if len(matches) == 0 {
		out.Status = "station_id_mismatch"
		return out
	}
	if !evidence.StationNamespaceConfirmed || evidence.OfficialReference == "" {
		out.Status = "id_namespace_unverified"
		return out
	}
	stationID := matches[0].ID
	out.StationID = &stationID
	for _, s := range matches {
		out.StationOrders = append(out.StationOrders, s.Order)
	}
	if len(matches) > 1 {
		out.Status = "station_sequence_ambiguous"
		return out
	}
	// Even one matching stop does not prove the aggregate's travel direction.
	out.Status = "station_id_matched_direction_unverified"
	return out
}
