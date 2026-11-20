package busstats

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ProviderRecord separates actual documented operating route count from trips.
type ProviderRecord struct {
	TimeWindow HourWindow `json:"timeWindow"`
	Record
	SourceRowNumber     *int64   `json:"sourceRowNumber"`
	StationNumber       *string  `json:"stationNumber"`
	RegionName          *string  `json:"regionName"`
	OperatingRouteCount *int64   `json:"operatingRouteCount"`
	DataCollectedDate   *string  `json:"dataCollectedDate"`
	ReviewReasonsList   []string `json:"reviewReasons"`
}
type Page struct {
	Status         string           `json:"status"`
	Code           string           `json:"code"`
	TotalCount     *int64           `json:"datasetTotalCount"`
	APIVersion     string           `json:"apiVersion"`
	ObservedFields []string         `json:"observedFields"`
	Records        []ProviderRecord `json:"records"`
	DuplicateCount int              `json:"duplicateCount"`
}
type resultMessage struct {
	Code string `json:"CODE"`
}

func safeCode(s string) string {
	parts := strings.Split(s, "-")
	if len(parts) != 2 || (parts[0] != "INFO" && parts[0] != "ERROR") || len(parts[1]) != 3 {
		return "unknown"
	}
	if _, e := strconv.Atoi(parts[1]); e != nil {
		return "unknown"
	}
	return s
}
func resultError(code string) error {
	kind := "api"
	switch code {
	case "ERROR-290", "INFO-300":
		kind = "authentication"
	case "ERROR-337":
		kind = "quota"
	case "ERROR-500", "ERROR-600", "ERROR-601":
		kind = "provider_server"
	}
	return &APIError{Kind: kind, Code: safeCode(code)}
}
func integer(raw json.RawMessage) (*int64, error) {
	b := bytes.TrimSpace(raw)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil, nil
	}
	// Real response uses JSON numbers such as 0.0. Do not round fractions or
	// coerce strings. big.Rat preserves exactness and avoids float64 precision loss.
	if b[0] == '"' {
		return nil, errors.New("통계 숫자 필드에 문자열이 있습니다")
	}
	n, ok := new(big.Rat).SetString(string(b))
	if !ok || !n.IsInt() || !n.Num().IsInt64() || n.Sign() < 0 {
		return nil, errors.New("통계 인원/횟수/행번호는 음수가 아닌 정확한 정수여야 합니다")
	}
	v := n.Num().Int64()
	return &v, nil
}
func textField(raw json.RawMessage) (*string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return nil, errors.New("통계 문자열 필드의 형식 오류")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	return &s, nil
}
func dateField(raw json.RawMessage) (*string, error) {
	s, e := textField(raw)
	if e != nil || s == nil {
		return s, e
	}
	if len(*s) != 8 {
		return nil, errors.New("운행일자/수집일자는 YYYYMMDD여야 합니다")
	}
	t, e := time.Parse("20060102", *s)
	if e != nil || t.Format("20060102") != *s {
		return nil, errors.New("유효하지 않은 통계 날짜")
	}
	v := t.Format("2006-01-02")
	return &v, nil
}
func normalizeRow(row map[string]json.RawMessage) (ProviderRecord, error) {
	out := ProviderRecord{}
	var e error
	for _, f := range []struct {
		k    string
		dest **string
	}{{"TMZON", &out.TimeSlot}, {"OPRT_ENTRPS_NM", &out.OperatorName}, {"ROUTE_NO", &out.RouteNumber}, {"STATION_ID", &out.StationID}, {"STATION_NM", &out.StationName}, {"REGION_DIV_NM", &out.RegionName}} {
		*f.dest, e = textField(row[f.k])
		if e != nil {
			return out, e
		}
	}
	for _, f := range []struct {
		k    string
		dest **int64
	}{{"NO", &out.SourceRowNumber}, {"RIDE_PSN_SUM", &out.BoardingCount}, {"QUIT_PSN_SUM", &out.AlightingCount}, {"OPRT_ROUTE_CNT", &out.OperatingRouteCount}} {
		*f.dest, e = integer(row[f.k])
		if e != nil {
			return out, e
		}
	}
	out.UseDate, e = dateField(row["OPRT_YMD"])
	if e != nil {
		return out, e
	}
	out.DataCollectedDate, e = dateField(row["DATA_COLCT_DE"])
	if e != nil {
		return out, e
	}
	stationNo, e := integer(row["STATION_NO"])
	if e != nil {
		return out, e
	}
	if stationNo != nil {
		v := strconv.FormatInt(*stationNo, 10)
		out.StationNumber = &v
	}
	// REGION_DIV_NM is a region, OPRT_ROUTE_CNT is route count. Neither proves
	// travel direction nor the number of operated trips.
	out.Direction = nil
	out.TripCount = nil
	if e = out.Record.Validate(); e != nil {
		return out, e
	}
	out.TimeWindow = InterpretTimeSlot(out.UseDate, out.TimeSlot)
	out.ReviewReasonsList = out.Record.ReviewReasons()
	if out.TimeWindow.Status != "applied" {
		out.ReviewReasonsList = append(out.ReviewReasonsList, "time_slot_"+out.TimeWindow.Status)
	}
	out.ReviewReasonsList = append(out.ReviewReasonsList, "trip_count_not_provided", "station_id_namespace_unverified")
	return out, nil
}

// DecodePage uses the actual first-page JSON envelope and UPPERCASE fields.
// The supplied XLSX lists lowercase fields; no lowercase source keys are guessed.
func DecodePage(raw []byte) (Page, error) {
	out := Page{Records: []ProviderRecord{}, ObservedFields: []string{}}
	var root map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) == 0 {
		return out, &APIError{Kind: "empty_response"}
	}
	if json.Unmarshal(raw, &root) != nil {
		return out, &APIError{Kind: "invalid_json"}
	}
	// Defensive top-level result handling: error fixtures follow the documented
	// codes; only the successful head/row envelope was observed live.
	if r, ok := root["RESULT"]; ok {
		var msg resultMessage
		if json.Unmarshal(r, &msg) != nil {
			return out, &APIError{Kind: "invalid_result"}
		}
		if msg.Code == "INFO-200" {
			out.Status = "empty"
			out.Code = msg.Code
			return out, nil
		}
		return out, resultError(msg.Code)
	}
	var sections []struct {
		Head []map[string]json.RawMessage `json:"head"`
		Rows []map[string]json.RawMessage `json:"row"`
	}
	body, ok := root[Service]
	if !ok || json.Unmarshal(body, &sections) != nil {
		return out, &APIError{Kind: "invalid_envelope"}
	}
	var rows []map[string]json.RawMessage
	headFound, rowFound := false, false
	for _, s := range sections {
		if s.Head != nil {
			if headFound {
				return out, &APIError{Kind: "duplicate_head"}
			}
			headFound = true
			for _, h := range s.Head {
				if r, ok := h["RESULT"]; ok {
					var msg resultMessage
					if json.Unmarshal(r, &msg) != nil {
						return out, &APIError{Kind: "invalid_result"}
					}
					out.Code = msg.Code
				}
				if n, ok := h["list_total_count"]; ok {
					var e error
					out.TotalCount, e = integer(n)
					if e != nil {
						return out, e
					}
				}
				if n, ok := h["api_version"]; ok {
					var v string
					if json.Unmarshal(n, &v) != nil {
						return out, &APIError{Kind: "invalid_api_version"}
					}
					out.APIVersion = v
				}
			}
		}
		if s.Rows != nil {
			if rowFound {
				return out, &APIError{Kind: "duplicate_rows"}
			}
			rowFound = true
			rows = s.Rows
		}
	}
	if !headFound || out.Code == "" {
		return out, &APIError{Kind: "missing_head"}
	}
	if out.Code == "INFO-200" {
		if len(rows) > 0 {
			return out, &APIError{Kind: "contradictory_empty_result"}
		}
		out.Status = "empty"
		return out, nil
	}
	if out.Code != "INFO-000" {
		return out, resultError(out.Code)
	}
	if !rowFound {
		return out, &APIError{Kind: "missing_rows"}
	}
	if out.TotalCount == nil || *out.TotalCount < int64(len(rows)) {
		return out, &APIError{Kind: "invalid_total_count"}
	}
	if len(rows) > PageSize {
		return out, &APIError{Kind: "page_limit_exceeded"}
	}
	fields := map[string]bool{}
	seen := map[int64]string{}
	for _, row := range rows {
		for k := range row {
			fields[k] = true
		}
		record, e := normalizeRow(row)
		if e != nil {
			return out, e
		}
		b, _ := json.Marshal(record)
		if record.SourceRowNumber != nil {
			if prior, ok := seen[*record.SourceRowNumber]; ok {
				if prior != string(b) {
					return out, errors.New("같은 페이지의 행번호 값 충돌")
				}
				out.DuplicateCount++
				continue
			}
			seen[*record.SourceRowNumber] = string(b)
		}
		out.Records = append(out.Records, record)
	}
	for k := range fields {
		out.ObservedFields = append(out.ObservedFields, k)
	}
	sort.Strings(out.ObservedFields)
	out.Status = "available"
	if len(out.Records) == 0 {
		out.Status = "empty"
	}
	return out, nil
}
