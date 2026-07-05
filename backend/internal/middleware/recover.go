package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recover converts a panic in any downstream handler into a clean 500 response
// (uniform error envelope) and logs the stack with the request ID. It is the last
// line of defence, never the error-handling strategy — handlers should return
// errors, not panic.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic recovered",
						"error", rec,
						"request_id", RequestIDFromContext(r.Context()),
						"method", r.Method,
						"path", r.URL.Path,
						"stack", string(debug.Stack()),
					)
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"An unexpected error occurred."}}`))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
