package middleware

import (
	"log/slog"
	"net"
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

// trustedProxyCount is how many reverse proxies (edge/LB) sit in front of the app;
// set once at startup via SetTrustedProxies. Read-only afterward.
var trustedProxyCount = 1

// SetTrustedProxies configures the number of trusted front proxies (clamped >=1).
func SetTrustedProxies(n int) {
	if n < 1 {
		n = 1
	}
	trustedProxyCount = n
}

// ClientIP returns the best-effort client IP. X-Forwarded-For is a list where each
// hop APPENDS the address it received from, so the rightmost entries are the ones
// our own trusted proxies added. We take the entry `trustedProxyCount` from the
// right — the address our infra vouches for — which a client cannot forge by
// prepending values (the old code trusted the leftmost, attacker-controlled entry).
// Falls back to the direct peer when XFF is absent or too short.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		n := trustedProxyCount
		if n > len(parts) {
			n = len(parts)
		}
		if ip := strings.TrimSpace(parts[len(parts)-n]); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
