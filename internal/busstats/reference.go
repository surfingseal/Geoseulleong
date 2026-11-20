package busstats

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"geoseulleong/internal/gbis"
)

type ReferenceStation struct {
	ID                 string `json:"stationId"`
	Name               string `json:"stationName"`
	Order              int    `json:"stationOrder"`
	Region             string `json:"region"`
	Turn               bool   `json:"isTurn"`
	ViaLabelDetected   bool   `json:"viaLabelDetected"`
	ReferenceDirection string `json:"referenceDirection"`
}
type IDOrders struct {
	ID     string `json:"stationId"`
	Orders []int  `json:"orders"`
}
type GBISReference struct {
	Source              string             `json:"source"`
	CollectedAt         time.Time          `json:"collectedAt"`
	RouteID             string             `json:"routeId"`
	RouteName           string             `json:"routeName"`
	TargetStationID     string             `json:"targetStationId"`
	TargetOrder         int                `json:"targetOrder"`
	TurnOrder           int                `json:"turnOrder"`
	HistoricalValidFrom *string            `json:"historicalValidFrom"`
	HistoricalValidTo   *string            `json:"historicalValidTo"`
	Stations            []ReferenceStation `json:"stations"`
	RepeatedIDs         []IDOrders         `json:"repeatedIds"`
	SameNameMultipleIDs []NameAmbiguity    `json:"sameNameMultipleIds"`
	MappingPerformed    bool               `json:"mappingPerformed"`
	Warnings            []string           `json:"warnings"`
}

// PrepareGBISReference reuses a saved, verified route and preserves every
// occurrence. It does not inspect or match statistics rows.
func PrepareGBISReference(raw []byte) (GBISReference, error) {
	out := GBISReference{Stations: []ReferenceStation{}, RepeatedIDs: []IDOrders{}, SameNameMultipleIDs: []NameAmbiguity{}, Warnings: []string{"방향은 GBIS 노선 순번/회차점 참조이며 통계 방향 코드가 확인된 것이 아닙니다.", "경유 표시는 원본 이름의 (경유) 문자열 탐지일 뿐 승하차 가능 여부를 확정하지 않습니다.", "현재 GBIS 참조가 과거 통계 운행일에도 유효했는지 별도 검증이 필요합니다."}}
	var b struct {
		Source   string         `json:"data_source"`
		Status   string         `json:"status"`
		At       time.Time      `json:"observed_at"`
		Route    *gbis.Route    `json:"route"`
		Stations []gbis.Station `json:"stations"`
		Selected *gbis.Station  `json:"selected_station"`
	}
	if json.Unmarshal(raw, &b) != nil || b.Source != "live_gbis_api" || b.Status != "success" || b.At.IsZero() || b.Route == nil || b.Route.ID != "234001317" || b.Route.Name != "M5121" || b.Selected == nil || b.Selected.ID != "203000294" || b.Selected.Order != 6 || b.Selected.TurnOrder == nil || *b.Selected.TurnOrder != 31 {
		return out, errors.New("검증된 실제 M5121 GBIS 결과가 아닙니다")
	}
	out.Source = b.Source
	out.CollectedAt = b.At
	out.RouteID = b.Route.ID
	out.RouteName = b.Route.Name
	out.TargetStationID = b.Selected.ID
	out.TargetOrder = 6
	out.TurnOrder = 31
	stations := append([]gbis.Station{}, b.Stations...)
	sort.Slice(stations, func(i, j int) bool { return stations[i].Order < stations[j].Order })
	seen := map[int]bool{}
	ids := map[string][]int{}
	names := map[string]map[string]bool{}
	targetFound, turnFound := false, false
	directionKnown := false
	if len(stations) > 0 && strings.Contains(stations[0].Region, "수원") {
		for _, s := range stations {
			if s.Order == 31 && strings.Contains(s.Region, "서울") {
				directionKnown = true
			}
		}
	}
	for _, s := range stations {
		if s.ID == "" || s.Name == "" || s.Order <= 0 || seen[s.Order] {
			return out, errors.New("GBIS 참조 정류장 누락/순번 중복")
		}
		seen[s.Order] = true
		if s.Order == 6 && s.ID == out.TargetStationID {
			targetFound = true
		}
		if s.Order == 31 {
			if s.Turn != "Y" {
				return out, errors.New("GBIS 회차점 표시 불일치")
			}
			turnFound = true
		} else if s.Turn == "Y" {
			return out, errors.New("GBIS 복수 회차점은 별도 검증이 필요합니다")
		}
		if s.TurnOrder != nil && *s.TurnOrder != 31 {
			return out, errors.New("GBIS 회차 순번 불일치")
		}
		d := "unknown"
		if directionKnown {
			switch {
			case s.Order < 31:
				d = "수원→서울"
			case s.Order == 31:
				d = "회차점"
			default:
				d = "서울→수원"
			}
		}
		out.Stations = append(out.Stations, ReferenceStation{ID: s.ID, Name: s.Name, Order: s.Order, Region: s.Region, Turn: s.Order == 31, ViaLabelDetected: strings.Contains(s.Name, "(경유)"), ReferenceDirection: d})
		ids[s.ID] = append(ids[s.ID], s.Order)
		if names[s.Name] == nil {
			names[s.Name] = map[string]bool{}
		}
		names[s.Name][s.ID] = true
	}
	if !targetFound || !turnFound {
		return out, errors.New("GBIS 참조에 대상/회차 정류장이 없습니다")
	}
	idKeys := map[string]bool{}
	for id := range ids {
		idKeys[id] = true
	}
	for _, id := range sortedSet(idKeys) {
		if len(ids[id]) > 1 {
			out.RepeatedIDs = append(out.RepeatedIDs, IDOrders{ID: id, Orders: ids[id]})
		}
	}
	nameKeys := map[string]bool{}
	for name := range names {
		nameKeys[name] = true
	}
	for _, name := range sortedSet(nameKeys) {
		if len(names[name]) > 1 {
			out.SameNameMultipleIDs = append(out.SameNameMultipleIDs, NameAmbiguity{Name: name, StationIDs: sortedSet(names[name])})
		}
	}
	return out, nil
}
