package middleware

import (
	"net/http"
	"time"
)

// WriteDeadline arms a fixed per-request write deadline on ordinary responses.
//
// The HTTP server runs with http.Server.WriteTimeout=0 because an absolute deadline
// set at request start kills SSE streams and long-polls (see httpx.ArmWriteDeadline).
// This middleware restores a bounded write deadline for normal request/response
// handlers, so a slow/stalled client cannot hold a write goroutine indefinitely.
// Streaming and long-poll handlers re-arm a longer rolling deadline
// (httpx.ArmWriteDeadline) immediately before they write, overriding this default.
//
// A non-positive d disables the middleware (no deadline is set).
func WriteDeadline(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if d > 0 {
				_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d))
			}
			next.ServeHTTP(w, r)
		})
	}
}
