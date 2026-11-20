package httpapi

import (
	"context"
	"errors"
	"geoseulleong/internal/alternatives"
	"geoseulleong/internal/odsay"
	"net/http"
	"strconv"
	"time"
)

type AlternativeReader interface {
	Search(context.Context, alternatives.Request) (alternatives.Result, error)
}

func alternativeRoutes(mux *http.ServeMux, a AlternativeReader) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		v := r.URL.Query()
		q := alternatives.Request{SearchID: v.Get("search_id"), Date: v.Get("service_date"), TZON: v.Get("tzon"), Window: 10}
		q.Recommend = r.URL.Path == "/api/v1/journeys/boarding-recommendations"
		if v.Has("origin_lon") || v.Has("origin_lat") {
			lon, e1 := strconv.ParseFloat(v.Get("origin_lon"), 64)
			lat, e2 := strconv.ParseFloat(v.Get("origin_lat"), 64)
			if e1 != nil || e2 != nil {
				failure(w, 400, "invalid_origin", "출발지 경도·위도를 함께 입력해 주세요.")
				return
			}
			q.Origin = &odsay.Point{Lon: lon, Lat: lat}
		}
		if q.Recommend {
			q.IncludeAccess = true
			q.IncludeReachability = true
		}
		if v.Has("include_access") {
			value, e := strconv.ParseBool(v.Get("include_access"))
			if e != nil {
				failure(w, 400, "invalid_include_access", "include_access는 true/false로 입력해 주세요.")
				return
			}
			q.IncludeAccess = value
		}
		if v.Has("include_reachability") {
			value, e := strconv.ParseBool(v.Get("include_reachability"))
			if e != nil {
				failure(w, 400, "invalid_include_reachability", "include_reachability는 true/false로 입력해 주세요.")
				return
			}
			q.IncludeReachability = value
		}
		if q.Recommend && (!q.IncludeAccess || !q.IncludeReachability) {
			failure(w, 400, "invalid_recommendation_options", "선택적 비교는 boarding-alternatives API를 사용해 주세요.")
			return
		}
		parse := func(key string, target *int) bool {
			if v.Get(key) == "" {
				return true
			}
			n, e := strconv.Atoi(v.Get(key))
			if e != nil {
				return false
			}
			*target = n
			return true
		}
		if !parse("path_index", &q.PathIndex) || !parse("leg_index", &q.LegIndex) || !parse("window", &q.Window) || !alternatives.Valid(q) {
			failure(w, 400, "invalid_alternative_query", "경로·구간 인덱스, 날짜, 원본 TZON 두 자리와 탐색 범위(1~20)를 확인해 주세요.")
			return
		}
		if a == nil {
			failure(w, 503, "alternatives_unavailable", "대체 승차 경로 조회가 구성되지 않았습니다.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		out, e := a.Search(ctx, q)
		if e != nil {
			if errors.Is(e, odsay.ErrSelectionStore) {
				failure(w, 503, "journey_selection_store_unavailable", "검색 선택 정보를 읽을 수 없습니다. 잠시 후 다시 시도해 주세요.")
			} else if errors.Is(e, alternatives.ErrSearchExpired) {
				failure(w, 410, "journey_selection_expired", "검색 결과가 만료되었습니다. 경로를 다시 조회해 주세요.")
			} else if errors.Is(e, alternatives.ErrInput) {
				failure(w, 400, "invalid_alternative_query", "검색 결과의 광역버스 구간 인덱스를 확인해 주세요.")
			} else if errors.Is(e, alternatives.ErrIdentity) {
				failure(w, 422, "boarding_identity_unverified", "공식 노선·정류장 ID나 방향을 확인할 수 없습니다.")
			} else {
				handleError(w, e)
			}
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		write(w, 200, out)
	}
	mux.HandleFunc("GET /api/v1/journeys/boarding-alternatives", handler)
	mux.HandleFunc("GET /api/v1/journeys/boarding-recommendations", handler)
}
