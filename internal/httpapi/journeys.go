package httpapi

import (
	"context"
	"errors"
	"geoseulleong/internal/odsay"
	"geoseulleong/internal/repository"
	"net/http"
	"strconv"
	"time"
)

type JourneyReader interface {
	Candidates(context.Context, odsay.Query) (odsay.Candidates, error)
}

func journeyRoutes(mux *http.ServeMux, reader JourneyReader) {
	mux.HandleFunc("GET /api/v1/journeys", func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		q := odsay.Query{EvaluationModel: values.Get("evaluation_model"), DepartureAt: values.Get("departure_at"), BoardingServiceDate: values.Get("boarding_service_date"), Mode: values.Get("mode"), Date: values.Get("reference_date"), Set: values.Get("analysis_set_id"), RawTimeCode: values.Get("reference_tzon")}
		if q.Mode == "" {
			q.Mode = "all"
		}
		fields := []struct {
			key    string
			target *float64
		}{{"start_lon", &q.Start.Lon}, {"start_lat", &q.Start.Lat}, {"end_lon", &q.End.Lon}, {"end_lat", &q.End.Lat}}
		for _, f := range fields {
			v, e := strconv.ParseFloat(values.Get(f.key), 64)
			if e != nil {
				failure(w, 400, "invalid_coordinates", "출발·도착 경도와 위도를 확인해 주세요.")
				return
			}
			*f.target = v
		}
		d, e := time.Parse("2006-01-02", q.Date)
		if !q.Valid() || e != nil || d.Format("2006-01-02") != q.Date || !setPattern.MatchString(q.Set) || q.RawTimeCode != "" && !rawTimePattern.MatchString(q.RawTimeCode) || values.Has("departure_time") || !validBoardingTime(q.DepartureAt, q.BoardingServiceDate) {
			failure(w, 400, "invalid_journey_query", "좌표·모드·참고일자·분석세트·원본 시간코드를 확인해 주세요. 선택 출발시각은 RFC3339, 운행일은 YYYY-MM-DD로 입력해 주세요.")
			return
		}
		if reader == nil {
			failure(w, 503, "routing_disabled", "경로 검색 실호출은 비활성 상태입니다.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 9*time.Second)
		defer cancel()
		response, e := reader.Candidates(ctx, q)
		if e != nil {
			switch {
			case errors.Is(e, odsay.ErrUnsupportedMode):
				failure(w, 422, "routing_mode_unavailable", "선택한 제공자는 이 이동수단을 지원하지 않습니다.")
			case errors.Is(e, odsay.ErrTimeout), errors.Is(e, context.DeadlineExceeded):
				failure(w, 504, "routing_timeout", "경로 조회 시간이 초과됐습니다. 자동 재시도하지 않습니다.")
			case errors.Is(e, odsay.ErrState):
				failure(w, 503, "routing_budget_state_unavailable", "경로 호출 예산을 확인할 수 없어 요청을 중단했습니다.")
			case errors.Is(e, odsay.ErrDisabled):
				failure(w, 503, "routing_disabled", "경로 검색 실호출은 비활성 상태입니다.")
			case errors.Is(e, odsay.ErrInput):
				failure(w, 400, "invalid_journey_query", "조회 입력을 확인해 주세요.")
			case errors.Is(e, repository.ErrAnalysisSet):
				failure(w, 404, "analysis_set_not_found", "요청한 분석세트가 없습니다.")
			case errors.Is(e, odsay.ErrProvider), errors.Is(e, odsay.ErrSchema):
				failure(w, 502, "routing_provider_error", "경로 제공 응답을 사용할 수 없습니다.")
			case errors.Is(e, odsay.ErrBudget):
				failure(w, 429, "routing_budget_exhausted", "승인된 호출 한도에 도달했습니다.")
			default:
				handleError(w, e)
			}
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		write(w, 200, response)
	})
}

func validBoardingTime(at, date string) bool {
	if at != "" {
		if _, e := time.Parse(time.RFC3339, at); e != nil {
			return false
		}
	}
	if date != "" {
		if _, e := time.Parse("2006-01-02", date); e != nil {
			return false
		}
	}
	return true
}
