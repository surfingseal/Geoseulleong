package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"geoseulleong/internal/repository"
	"net/http"
	"regexp"
)

type RouteReader interface {
	RouteByName(context.Context, string) (repository.Route, error)
	Stops(context.Context, int64) ([]repository.Stop, error)
	Shape(context.Context, int64) (json.RawMessage, error)
}

var routeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

func New(reader RouteReader) http.Handler { return NewWithCongestion(reader, nil) }
func NewWithCongestion(reader RouteReader, c CongestionReader) http.Handler {
	return NewWithJourneys(reader, c, nil)
}
func NewWithJourneys(reader RouteReader, c CongestionReader, j JourneyReader) http.Handler {
	return NewWithAlternatives(reader, c, j, nil)
}
func NewWithAlternatives(reader RouteReader, c CongestionReader, j JourneyReader, a AlternativeReader) http.Handler {
	mux := http.NewServeMux()
	alternativeRoutes(mux, a)
	congestionRoutes(mux, c)
	journeyRoutes(mux, j)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	for _, suffix := range []string{"", "/stops", "/shape"} {
		kind := suffix
		mux.HandleFunc("GET /api/routes/{route}"+kind, func(w http.ResponseWriter, r *http.Request) {
			name := r.PathValue("route")
			if !routeName.MatchString(name) {
				failure(w, 400, "invalid_route", "노선명이 올바르지 않습니다.")
				return
			}
			route, e := reader.RouteByName(r.Context(), name)
			if e != nil {
				handleError(w, e)
				return
			}
			switch kind {
			case "":
				write(w, 200, route)
			case "/stops":
				stops, e := reader.Stops(r.Context(), route.ID)
				if e != nil {
					handleError(w, e)
					return
				}
				write(w, 200, map[string]any{"route": route, "stops": stops})
			case "/shape":
				shape, e := reader.Shape(r.Context(), route.ID)
				if e != nil {
					handleError(w, e)
					return
				}
				w.Header().Set("Content-Type", "application/geo+json; charset=utf-8")
				write(w, 200, shape)
			}
		})
	}
	// Route/method misses also use the same safe JSON error envelope.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, p := mux.Handler(r)
		if p == "" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				failure(w, 405, "method_not_allowed", "허용되지 않는 요청 방식입니다.")
				return
			}
			failure(w, 404, "not_found", "요청한 API가 없습니다.")
			return
		}
		// ServeMux.ServeHTTP is required to populate PathValue.
		_ = h
		mux.ServeHTTP(w, r)
	})
}
func handleError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, repository.ErrNotFound):
		failure(w, 404, "not_found", "노선 또는 경로 정보가 없습니다.")
	case errors.Is(e, repository.ErrAmbiguous):
		failure(w, 409, "ambiguous_route", "동일한 노선명이 여러 개입니다.")
	case errors.Is(e, repository.ErrInvalidData):
		failure(w, 500, "invalid_stored_data", "저장된 정보를 읽을 수 없습니다.")
	default:
		failure(w, 503, "database_unavailable", "데이터 조회에 실패했습니다. 잠시 후 다시 시도해 주세요.")
	}
}
func failure(w http.ResponseWriter, status int, code, message string) {
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func write(w http.ResponseWriter, status int, v any) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
