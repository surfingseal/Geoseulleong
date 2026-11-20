// Package busstats defines an offline normalized contract and conservative
// matching rules. It deliberately contains no unverified provider API parser.
package busstats

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Record field names are our normalized schema, NOT provider field names.
// Dates use ISO format once a future verified adapter knows the source format.
// TimeSlot preserves the source label; no hourly interpretation is assumed.
type Record struct {
	UseDate        *string `json:"useDate"`
	TimeSlot       *string `json:"timeSlot"`
	RouteNumber    *string `json:"routeNumber"`
	OperatorName   *string `json:"operatorName"`
	StationID      *string `json:"stationId"`
	StationName    *string `json:"stationName"`
	BoardingCount  *int64  `json:"boardingCount"`
	AlightingCount *int64  `json:"alightingCount"`
	Direction      *string `json:"direction"`
	TripCount      *int64  `json:"tripCount"`
}

func (r Record) Validate() error {
	if r.UseDate != nil {
		t, e := time.Parse("2006-01-02", *r.UseDate)
		if e != nil || t.Format("2006-01-02") != *r.UseDate {
			return errors.New("사용일자는 정규화된 ISO 날짜(YYYY-MM-DD)여야 합니다")
		}
	}
	for _, v := range []*int64{r.BoardingCount, r.AlightingCount, r.TripCount} {
		if v != nil && *v < 0 {
			return errors.New("승하차/운행 횟수는 음수일 수 없습니다")
		}
	}
	for _, s := range []*string{r.TimeSlot, r.RouteNumber, r.OperatorName, r.StationID, r.StationName, r.Direction} {
		if s != nil && (strings.TrimSpace(*s) == "" || len(*s) > 500) {
			return errors.New("정규화 문자열이 비어 있거나 너무 깁니다")
		}
	}
	return nil
}
func (r Record) ReviewReasons() []string {
	a := []string{}
	for _, f := range []struct {
		name  string
		value *string
	}{{"use_date_missing", r.UseDate}, {"time_slot_missing", r.TimeSlot}, {"route_number_missing", r.RouteNumber}, {"station_id_missing", r.StationID}, {"direction_missing", r.Direction}} {
		if f.value == nil {
			a = append(a, f.name)
		}
	}
	if r.BoardingCount == nil {
		a = append(a, "boarding_count_missing")
	}
	if r.AlightingCount == nil {
		a = append(a, "alighting_count_missing")
	}
	return a
}

// Deduplicate removes exact duplicates, keeps companies/directions separate,
// and rejects conflicting values in the same aggregate bucket. It never sums.
func Deduplicate(rows []Record) ([]Record, int, error) {
	out := []Record{}
	seen := map[string]string{}
	dups := 0
	for i, r := range rows {
		if e := r.Validate(); e != nil {
			return nil, 0, fmt.Errorf("정규화 레코드 %d: %w", i+1, e)
		}
		// Incomplete bucket identifiers cannot establish identity; retain each row.
		if r.UseDate == nil || r.TimeSlot == nil || r.RouteNumber == nil || r.StationID == nil || r.Direction == nil || r.OperatorName == nil {
			out = append(out, r)
			continue
		}
		key, _ := json.Marshal([]*string{r.UseDate, r.TimeSlot, r.RouteNumber, r.StationID, r.Direction, r.OperatorName})
		raw, _ := json.Marshal(r)
		if old, ok := seen[string(key)]; ok {
			if old != string(raw) {
				return nil, 0, errors.New("같은 집계 구간의 통계 값 충돌: 자동 합산하지 않습니다")
			}
			dups++
			continue
		}
		seen[string(key)] = string(raw)
		out = append(out, r)
	}
	return out, dups, nil
}
