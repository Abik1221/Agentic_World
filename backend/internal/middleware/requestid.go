package middleware

import (
	"context"
	"net/http"

	"github.com/agent-arena/arena/internal/platform"
)

type ctxKey int

const requestIDKey ctxKey = iota

// HeaderRequestID is the inbound/outbound correlation header.
const HeaderRequestID = "X-Request-ID"

// RequestID ensures every request carries a correlation ID: it reuses an inbound
// X-Request-ID when present (trusted edge/LB), otherwise generates one. The ID is
// stored in the context and echoed in the response header so logs, traces, and
// client reports all line up.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if id == "" {
			id = platform.NewID("req")
		}
		w.Header().Set(HeaderRequestID, id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext returns the correlation ID, or "" if unset.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}
