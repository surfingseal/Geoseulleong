package httpapi

import (
	"context"
	"net/http"
	"time"
)

type healthPinger interface{ Ping(context.Context) error }

// WithHealth keeps /healthz and all existing API routes intact.
func WithHealth(next http.Handler, db healthPinger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			write(w, 405, map[string]string{"status": "method_not_allowed"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if db == nil || db.Ping(ctx) != nil {
			write(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		write(w, 200, map[string]string{"status": "ok", "database": "connected"})
	})
}
