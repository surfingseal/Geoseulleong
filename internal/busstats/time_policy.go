package busstats

import (
	"fmt"
	"strconv"
	"time"
	_ "time/tzdata"
)

const TimeSlotPolicy = "tmzon_service_hour_v1"
const TimeSlotPolicySource = "user_instruction_2026-11-11"

// HourWindow applies the user-approved service-day hour interpretation. It is
// a project policy, not a claim that the provider officially defined TMZON.
type HourWindow struct {
	Policy                     string     `json:"policy"`
	Source                     string     `json:"source"`
	OfficialDefinitionVerified bool       `json:"officialDefinitionVerified"`
	RawCode                    *string    `json:"rawCode"`
	ServiceDate                *string    `json:"serviceDate"`
	Timezone                   string     `json:"timezone"`
	Applied                    bool       `json:"applied"`
	Status                     string     `json:"status"`
	ServiceHour                *int       `json:"serviceHour"`
	HourOfDay                  *int       `json:"hourOfDay"`
	DayOffset                  *int       `json:"dayOffset"`
	EndDayOffset               *int       `json:"endDayOffset"`
	StartTime                  *string    `json:"startTime"`
	EndTime                    *string    `json:"endTime"`
	StartAt                    *time.Time `json:"startAt"`
	EndAt                      *time.Time `json:"endAt"`
	EndExclusive               bool       `json:"endExclusive"`
}

// InterpretTimeSlot treats 0..47 as one-hour service-day slots in Asia/Seoul.
// Codes outside the supported two-day range remain raw, without guessed dates.
func InterpretTimeSlot(useDate, rawCode *string) HourWindow {
	out := HourWindow{Policy: TimeSlotPolicy, Source: TimeSlotPolicySource, RawCode: rawCode, ServiceDate: useDate, Timezone: "Asia/Seoul", Status: "missing_code", EndExclusive: true}
	if rawCode == nil {
		return out
	}
	if len(*rawCode) == 0 || len(*rawCode) > 2 {
		out.Status = "invalid_code"
		return out
	}
	for _, c := range *rawCode {
		if c < '0' || c > '9' {
			out.Status = "invalid_code"
			return out
		}
	}
	h, e := strconv.Atoi(*rawCode)
	if e != nil {
		out.Status = "invalid_code"
		return out
	}
	if h > 47 {
		out.Status = "unsupported_hour"
		return out
	}
	hour, offset, endOffset := h%24, h/24, (h+1)/24
	start, end := fmt.Sprintf("%02d:00", hour), fmt.Sprintf("%02d:00", (h+1)%24)
	out.ServiceHour = &h
	out.HourOfDay = &hour
	out.DayOffset = &offset
	out.EndDayOffset = &endOffset
	out.StartTime = &start
	out.EndTime = &end
	out.Applied = true
	out.Status = "missing_service_date"
	if useDate == nil {
		return out
	}
	d, e := time.Parse("2006-01-02", *useDate)
	if e != nil || d.Format("2006-01-02") != *useDate {
		out.Applied = false
		out.Status = "invalid_service_date"
		return out
	}
	loc, e := time.LoadLocation("Asia/Seoul")
	if e != nil {
		out.Applied = false
		out.Status = "timezone_unavailable"
		return out
	}
	day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	startAt := day.AddDate(0, 0, offset).Add(time.Duration(hour) * time.Hour)
	endAt := day.AddDate(0, 0, endOffset).Add(time.Duration((h+1)%24) * time.Hour)
	if startAt.Year() > 9999 || endAt.Year() > 9999 {
		out.Applied = false
		out.Status = "date_out_of_range"
		return out
	}
	out.StartAt = &startAt
	out.EndAt = &endAt
	out.Status = "applied"
	return out
}
