package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type testPinger struct{ err error }

func (p testPinger) Ping(context.Context) error { return p.err }
func TestHealth(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{{nil, 200}, {errors.New("secret DSN"), 503}} {
		handler := WithHealth(http.NotFoundHandler(), testPinger{tc.err})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
		if w.Code != tc.want {
			t.Fatal(w.Code)
		}
		if tc.err != nil && w.Body.String() != "{\"status\":\"unavailable\"}\n" {
			t.Fatal("unexpected health body")
		}
	}
}
