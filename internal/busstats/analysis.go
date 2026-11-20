package busstats

import (
	"errors"
	"math"
	"sort"
)

// SampleCount is a sum of observed aggregate records, not a passenger forecast.
type SampleCount struct {
	Sum         *int64 `json:"sum"`
	PresentRows int    `json:"presentRows"`
	MissingRows int    `json:"missingRows"`
}
type StationAudit struct {
	StationID        string   `json:"stationId"`
	Rows             int      `json:"rows"`
	Names            []string `json:"names"`
	Numbers          []string `json:"stationNumbers"`
	Regions          []string `json:"regions"`
	MetadataConflict bool     `json:"metadataConflict"`
}
type NameAmbiguity struct {
	Name       string   `json:"name"`
	StationIDs []string `json:"stationIds"`
}
type TimeCodeDefinition struct {
	Applied              bool    `json:"applied"`
	Policy               string  `json:"policy"`
	DayOffset            *int    `json:"dayOffset"`
	EndDayOffset         *int    `json:"endDayOffset"`
	Timezone             string  `json:"timezone"`
	RawCode              string  `json:"rawCode"`
	Verified             bool    `json:"verified"`
	StartTime            *string `json:"startTime"`
	EndTime              *string `json:"endTime"`
	CalendarDateBoundary *string `json:"calendarDateBoundary"`
	DefinitionSource     *string `json:"definitionSource"`
}
type SampleMetrics struct {
	Rows      int         `json:"rows"`
	Boarding  SampleCount `json:"boarding"`
	Alighting SampleCount `json:"alighting"`
}
type SampleAnalysis struct {
	RowsByCalendarHour       map[string]int       `json:"rowsByCalendarHour"`
	UnresolvedTimeRows       int                  `json:"unresolvedTimeRows"`
	TimeInterpretationPolicy string               `json:"timeInterpretationPolicy"`
	Scope                    string               `json:"scope"`
	DatasetTotalCount        *int64               `json:"datasetTotalCount"`
	SampleRows               int                  `json:"sampleRows"`
	ProviderRowDuplicates    int                  `json:"providerRowDuplicates"`
	RowsByRoute              map[string]int       `json:"rowsByRoute"`
	RowsByUseDate            map[string]int       `json:"rowsByUseDate"`
	RowsByRawTimeCode        map[string]int       `json:"rowsByRawTimeCode"`
	MissingFields            map[string]int       `json:"missingFields"`
	UnprovidedFields         map[string]int       `json:"unprovidedFields"`
	Totals                   SampleMetrics        `json:"sampleTotals"`
	RepeatedStationIDs       []StationAudit       `json:"repeatedStationIds"`
	StationMetadataConflicts []StationAudit       `json:"stationMetadataConflicts"`
	SameNameMultipleIDs      []NameAmbiguity      `json:"sameNameMultipleIds"`
	TimeCodes                []TimeCodeDefinition `json:"timeCodeDefinitions"`
	M5121RowsInSample        int                  `json:"m5121RowsInSample"`
	M5121ExistsInDataset     *bool                `json:"m5121ExistsInDataset"`
	M5121Metrics             *SampleMetrics       `json:"m5121Metrics"`
	MappingAttempted         bool                 `json:"mappingAttempted"`
	MappingBlockers          []string             `json:"mappingBlockers"`
	Warnings                 []string             `json:"warnings"`
}

func sumObserved(total *SampleCount, value *int64) error {
	if value == nil {
		total.MissingRows++
		return nil
	}
	if *value < 0 {
		return errors.New("관측 집계 값은 음수일 수 없습니다")
	}
	if total.Sum == nil {
		v := int64(0)
		total.Sum = &v
	}
	if *value > math.MaxInt64-*total.Sum {
		return errors.New("승하차 집계 합계 int64 범위 초과")
	}
	*total.Sum += *value
	total.PresentRows++
	return nil
}
func addMetrics(m *SampleMetrics, r ProviderRecord) error {
	m.Rows++
	if e := sumObserved(&m.Boarding, r.BoardingCount); e != nil {
		return e
	}
	return sumObserved(&m.Alighting, r.AlightingCount)
}
func sortedSet(m map[string]bool) []string {
	a := make([]string, 0, len(m))
	for s := range m {
		a = append(a, s)
	}
	sort.Strings(a)
	return a
}
func missing(m map[string]int, k string, isMissing bool) {
	if _, ok := m[k]; !ok {
		m[k] = 0
	}
	if isMissing {
		m[k]++
	}
}

// AnalyzeSample is offline-only. It describes this saved page and never turns
// an absent sample route into dataset-wide absence or auto-matches a stop.
func AnalyzeSample(page Page) (SampleAnalysis, error) {
	out := SampleAnalysis{RowsByCalendarHour: map[string]int{}, TimeInterpretationPolicy: TimeSlotPolicy, Scope: "saved_page_only", DatasetTotalCount: page.TotalCount, SampleRows: len(page.Records), ProviderRowDuplicates: page.DuplicateCount, RowsByRoute: map[string]int{}, RowsByUseDate: map[string]int{}, RowsByRawTimeCode: map[string]int{}, MissingFields: map[string]int{}, UnprovidedFields: map[string]int{}, RepeatedStationIDs: []StationAudit{}, StationMetadataConflicts: []StationAudit{}, SameNameMultipleIDs: []NameAmbiguity{}, TimeCodes: []TimeCodeDefinition{}, MappingBlockers: []string{"official_station_id_namespace_unverified", "official_direction_definition_unverified"}, Warnings: []string{"합계는 이 페이지에 관측된 집계 레코드의 합이며 M5121 전체 수요나 고유 승객 수가 아닙니다.", "정류장 ID의 반복은 다른 노선/시간대에서 정상적으로 나타날 수 있으며 승하차 행을 자동 합치지 않습니다.", "같은 ID의 명칭 차이는 과거 명칭 변경일 수도 있어 검토 후보이지 자동 오류 확정이 아닙니다.", "TMZON은 원본을 보존하며 사용자 지정 영업일 시간대 정책으로 해석합니다. 공식 정의가 검증된 것으로 표시하지 않습니다."}}
	type stationState struct {
		count                   int
		names, numbers, regions map[string]bool
	}
	stations := map[string]*stationState{}
	names := map[string]map[string]bool{}
	for _, r := range page.Records {
		if e := r.Record.Validate(); e != nil {
			return out, e
		}
		w := r.TimeWindow
		if w.Policy == "" {
			w = InterpretTimeSlot(r.UseDate, r.TimeSlot)
		}
		if w.Status == "applied" && w.StartAt != nil {
			out.RowsByCalendarHour[w.StartAt.Format("2006-01-02T15:04:05Z07:00")]++
		} else {
			out.UnresolvedTimeRows++
		}
		if r.RouteNumber != nil {
			out.RowsByRoute[*r.RouteNumber]++
		}
		if r.UseDate != nil {
			out.RowsByUseDate[*r.UseDate]++
		}
		if r.TimeSlot != nil {
			out.RowsByRawTimeCode[*r.TimeSlot]++
		}
		for _, f := range []struct {
			name   string
			absent bool
		}{
			{"OPRT_YMD", r.UseDate == nil}, {"TMZON", r.TimeSlot == nil}, {"OPRT_ENTRPS_NM", r.OperatorName == nil}, {"ROUTE_NO", r.RouteNumber == nil}, {"STATION_ID", r.StationID == nil}, {"STATION_NM", r.StationName == nil}, {"STATION_NO", r.StationNumber == nil}, {"REGION_DIV_NM", r.RegionName == nil}, {"NO", r.SourceRowNumber == nil}, {"DATA_COLCT_DE", r.DataCollectedDate == nil}, {"RIDE_PSN_SUM", r.BoardingCount == nil}, {"QUIT_PSN_SUM", r.AlightingCount == nil}, {"OPRT_ROUTE_CNT", r.OperatingRouteCount == nil},
		} {
			missing(out.MissingFields, f.name, f.absent)
		}
		missing(out.UnprovidedFields, "direction", r.Direction == nil)
		missing(out.UnprovidedFields, "tripCount", r.TripCount == nil)
		if e := addMetrics(&out.Totals, r); e != nil {
			return out, e
		}
		if r.RouteNumber != nil && *r.RouteNumber == "M5121" {
			out.M5121RowsInSample++
			if out.M5121Metrics == nil {
				out.M5121Metrics = &SampleMetrics{}
			}
			if e := addMetrics(out.M5121Metrics, r); e != nil {
				return out, e
			}
		}
		if r.StationID != nil {
			id := *r.StationID
			s := stations[id]
			if s == nil {
				s = &stationState{names: map[string]bool{}, numbers: map[string]bool{}, regions: map[string]bool{}}
				stations[id] = s
			}
			s.count++
			if r.StationName != nil {
				s.names[*r.StationName] = true
				if names[*r.StationName] == nil {
					names[*r.StationName] = map[string]bool{}
				}
				names[*r.StationName][id] = true
			}
			if r.StationNumber != nil {
				s.numbers[*r.StationNumber] = true
			}
			if r.RegionName != nil {
				s.regions[*r.RegionName] = true
			}
		}
	}
	ids := map[string]bool{}
	for id := range stations {
		ids[id] = true
	}
	for _, id := range sortedSet(ids) {
		s := stations[id]
		audit := StationAudit{StationID: id, Rows: s.count, Names: sortedSet(s.names), Numbers: sortedSet(s.numbers), Regions: sortedSet(s.regions), MetadataConflict: len(s.names) > 1 || len(s.numbers) > 1 || len(s.regions) > 1}
		if s.count > 1 {
			out.RepeatedStationIDs = append(out.RepeatedStationIDs, audit)
		}
		if audit.MetadataConflict {
			out.StationMetadataConflicts = append(out.StationMetadataConflicts, audit)
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
	codes := map[string]bool{}
	for code := range out.RowsByRawTimeCode {
		codes[code] = true
	}
	for _, code := range sortedSet(codes) {
		window := InterpretTimeSlot(nil, &code)
		source := window.Source
		boundary := "service_date_plus_day_offset"
		def := TimeCodeDefinition{RawCode: code, Verified: false, Applied: window.Applied, Policy: window.Policy, DayOffset: window.DayOffset, EndDayOffset: window.EndDayOffset, Timezone: window.Timezone, DefinitionSource: &source, StartTime: window.StartTime, EndTime: window.EndTime}
		if window.Applied {
			def.CalendarDateBoundary = &boundary
		}
		out.TimeCodes = append(out.TimeCodes, def)
	}
	if out.M5121RowsInSample > 0 {
		yes := true
		out.M5121ExistsInDataset = &yes
	} else {
		out.MappingBlockers = append([]string{"no_m5121_rows_in_saved_page"}, out.MappingBlockers...)
	}
	// MappingAttempted stays false. This function audits the sample only.
	return out, nil
}
