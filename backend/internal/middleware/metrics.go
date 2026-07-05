package middleware

import (
	"net/http"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/go-chi/chi/v5"
)

// Metrics records RED (Rate/Errors/Duration) instruments per request, labelled by
// the chi *route pattern* (e.g. "/v1/match/{id}/state") rather than the concrete
// path, to keep Prometheus cardinality bounded.
func Metrics(m *platform.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}

			next.ServeHTTP(sw, r)

			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			m.ObserveHTTP(r.Method, route, sw.Status(), time.Since(start))
		})
	}
}
