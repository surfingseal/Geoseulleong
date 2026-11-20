// Package boarding adds independent historical and current-seat context, never a risk score.
package boarding

import (
	"context"
	"geoseulleong/internal/busstats"
	"strconv"
	"time"
)

type Stop struct {
	PermissionStatus string `json:"boarding_permission_status,omitempty"`
	VisitDirection   string `json:"route_visit_direction,omitempty"`
	RouteID          int64  `json:"internal_route_id"`
	VisitID          int64  `json:"visit_id"`
	GBISRoute        string `json:"gbis_route_id"`
	GBISStop         string `json:"gbis_stop_id"`
	Sequence         int    `json:"gbis_sequence"`
	STCISRoute       string `json:"stcis_route_id"`
	STCISStop        string `json:"stcis_stop_id"`
	SourceSequence   int    `json:"stcis_sequence"`
	MappingDate      string `json:"verified_reference_date"`
}
type Request struct {
	RouteID     int64
	VisitID     int64
	GBISRoute   string
	GBISStop    string
	BoardingAt  time.Time
	ServiceDate string
}
type TimeContext struct {
	Status           string     `json:"status"`
	BoardingAt       *time.Time `json:"estimated_boarding_at"`
	ServiceDate      string     `json:"service_date,omitempty"`
	TZON             string     `json:"tzon,omitempty"`
	Policy           string     `json:"policy"`
	OfficialVerified bool       `json:"official_definition_verified"`
}

// ServiceDate is explicit: 03:00 can belong to code03 or previous-day code27.
func MatchTime(at time.Time, serviceDate string) TimeContext {
	out := TimeContext{Status: "unknown", Policy: "project_service_hour_v1_user_instruction"}
	if at.IsZero() {
		return out
	}
	out.BoardingAt = &at
	loc, _ := time.LoadLocation("Asia/Seoul")
	d, e := time.ParseInLocation("2006-01-02", serviceDate, loc)
	if e != nil {
		return out
	}
	h := int(at.In(loc).Sub(d) / time.Hour)
	if at.Before(d) || h < 0 || h > 47 {
		return out
	}
	code := strconv.Itoa(h)
	w := busstats.InterpretTimeSlot(&serviceDate, &code)
	if !w.Applied {
		return out
	}
	out.Status = "matched_project_rule"
	out.ServiceDate = serviceDate
	out.TZON = code
	return out
}

type Profile struct {
	DayGroup     string   `json:"day_group"`
	Mean         *float64 `json:"average_percent"`
	P90          *float64 `json:"p90_percent"`
	Observations int64    `json:"observation_count"`
	NumericCount int64    `json:"numeric_count"`
	UniqueJSON   int64    `json:"unique_json_count"`
	ObservedDays int      `json:"observed_day_count"`
	FirstDate    string   `json:"first_service_date"`
	LastDate     string   `json:"last_service_date"`
	Dates        []string `json:"observed_dates"`
	BatchIDs     []string `json:"batch_ids"`
	LowSample    bool     `json:"low_sample"`
}
type History struct {
	Status            string    `json:"status"`
	Groups            []Profile `json:"groups"`
	SetIDs            []string  `json:"analysis_set_ids"`
	Unit              string    `json:"unit"`
	Interpretation    string    `json:"interpretation_source"`
	HistoricalMapping string    `json:"historical_mapping_status"`
}
type SeatVehicle struct {
	ArrivalIndex int     `json:"arrival_index"`
	VehicleID    *string `json:"vehicle_id"`
	PlateNo      *string `json:"plate_no"`
	ETA          *int    `json:"eta_minutes"`
	ETASeconds   *int    `json:"eta_seconds"`
	Seats        *int    `json:"remaining_seats"`
	Status       string  `json:"status"`
}
type Realtime struct {
	Status            string        `json:"status"`
	Origin            string        `json:"data_origin"`
	ReceivedAt        *time.Time    `json:"received_at"`
	ProviderUpdatedAt *time.Time    `json:"provider_updated_at"`
	Vehicles          []SeatVehicle `json:"vehicles"`
	Meaning           string        `json:"meaning"`
}
type Analysis struct {
	Status   string      `json:"status"`
	Stop     *Stop       `json:"stop,omitempty"`
	Time     TimeContext `json:"time"`
	History  History     `json:"historical_congestion"`
	Realtime Realtime    `json:"realtime_seat"`
	Notes    []string    `json:"notes"`
}
type Resolver interface {
	ResolveBoarding(context.Context, Request) (Stop, error)
}
type HistoryReader interface {
	BoardingHistory(context.Context, Stop, TimeContext) (History, error)
}
type SeatReader interface {
	Seats(context.Context, Stop) Realtime
}
type Service struct {
	Resolver Resolver
	History  HistoryReader
	Realtime SeatReader
}

func (s *Service) Analyze(ctx context.Context, r Request) Analysis {
	out := Analysis{Status: "unavailable", Time: MatchTime(r.BoardingAt, r.ServiceDate), History: History{Status: "unavailable", Groups: []Profile{}}, Realtime: Realtime{Status: "disabled", Vehicles: []SeatVehicle{}, Meaning: "current_reported_vehicle_seats_not_future_boarding_guarantee"}, Notes: []string{"no_probability_or_time_penalty", "boarding_permission_not_inferred", "historical_reference_not_prediction", "boarding_time_estimated_from_preceding_provider_sections"}}
	if s.Resolver == nil {
		out.Status = "mapping_unverified"
		return out
	}
	stop, e := s.Resolver.ResolveBoarding(ctx, r)
	if e != nil {
		out.Status = "mapping_unverified"
		return out
	}
	out.Stop = &stop
	if s.History != nil && out.Time.Status == "matched_project_rule" {
		h, e := s.History.BoardingHistory(ctx, stop, out.Time)
		if e != nil {
			out.History.Status = "query_error"
		} else {
			out.History = h
		}
	} else if out.Time.Status == "unknown" {
		out.History.Status = "time_unknown"
	}
	if s.Realtime != nil {
		out.Realtime = s.Realtime.Seats(ctx, stop)
	}
	h, rt := out.History.Status == "available", out.Realtime.Status == "available"
	switch {
	case h && rt:
		out.Status = "history_and_realtime"
	case h:
		out.Status = "history_only"
	case rt:
		out.Status = "realtime_only"
	}
	return out
}
