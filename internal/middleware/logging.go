package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Logging emits one structured log line per request with method, path, status,
// duration, bytes, client IP, and the correlation ID. Server errors log at error
// level, everything else at info.
func Logging(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}

			next.ServeHTTP(sw, r)

			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.Status(),
				"duration_ms", time.Since(start).Milliseconds(),
				"bytes", sw.written,
				"request_id", RequestIDFromContext(r.Context()),
				"remote", ClientIP(r),
			}
			if sw.Status() >= http.StatusInternalServerError {
				log.Error("request", attrs...)
			} else {
				log.Info("request", attrs...)
			}
		})
	}
}

// ClientIP returns the best-effort client IP, honoring X-Forwarded-For set by a
// trusted edge/LB. Used for logging, captcha, and rate-limit keys.
func ClientIP(r *http.Request) string {
	// Trust the edge/LB to set X-Forwarded-For; take the first hop if present.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	return r.RemoteAddr
}
