package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"geoseulleong/internal/repository"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

type CongestionReader interface {
	RouteProfile(context.Context, int64, string, string) (json.RawMessage, error)
	VisitProfile(context.Context, int64, int64, string, string, string) (json.RawMessage, error)
}

var setPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var rawTimePattern = regexp.MustCompile(`^[0-9]{2}$`)

func congestionRoutes(mux *http.ServeMux, c CongestionReader) {
	if c == nil {
		return
	}
	for _, suffix := range []string{"", "/visits/{visitId}"} {
		kind := suffix
		mux.HandleFunc("GET /api/v1/routes/{routeId}/congestion"+kind, func(w http.ResponseWriter, r *http.Request) {
			routeID, e := strconv.ParseInt(r.PathValue("routeId"), 10, 64)
			if e != nil || routeID < 1 {
				failure(w, 400, "invalid_route_id", "내부 노선 ID가 올바르지 않습니다.")
				return
			}
			q := r.URL.Query()
			date, set, tzon := q.Get("date"), q.Get("analysis_set_id"), q.Get("tzon")
			parsed, e := time.Parse("2006-01-02", date)
			if e != nil || parsed.Format("2006-01-02") != date || !setPattern.MatchString(set) || tzon != "" && !rawTimePattern.MatchString(tzon) {
				failure(w, 400, "invalid_analysis_query", "date와 analysis_set_id, 원본 시간코드를 확인해 주세요.")
				return
			}
			var result json.RawMessage
			if kind == "" {
				if tzon != "" {
					failure(w, 400, "invalid_analysis_query", "시간코드는 정류장 방문 조회에서 사용해 주세요.")
					return
				}
				result, e = c.RouteProfile(r.Context(), routeID, date, set)
			} else {
				visit, e2 := strconv.ParseInt(r.PathValue("visitId"), 10, 64)
				if e2 != nil || visit < 1 {
					failure(w, 400, "invalid_visit_id", "노선 방문 ID가 올바르지 않습니다.")
					return
				}
				result, e = c.VisitProfile(r.Context(), routeID, visit, date, set, tzon)
			}
			if e != nil {
				switch {
				case errors.Is(e, repository.ErrAnalysisSet):
					failure(w, 404, "analysis_set_not_found", "요청한 노선·날짜의 분석세트가 없습니다.")
				case errors.Is(e, repository.ErrVisit):
					failure(w, 404, "visit_not_found", "해당 노선 방문이 없습니다.")
				default:
					handleError(w, e)
				}
				return
			}
			write(w, 200, result)
		})
	}
}
