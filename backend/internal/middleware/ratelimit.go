package middleware

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// Limiter is the backend for rate limiting (a Redis sliding/fixed window lives in
// internal/store). It is an interface here so middleware never imports a driver.
type Limiter interface {
	// Allow reports whether the action under key is within `limit` per `window`.
	// When denied it returns the duration until a slot frees (for Retry-After).
	Allow(ctx context.Context, key string, limit int, window time.Duration) (ok bool, retryAfter time.Duration, err error)
}

// KeyFunc derives the rate-limit bucket key for a request.
type KeyFunc func(*http.Request) string

// IPKey buckets by client IP under a namespace, e.g. IPKey("register").
func IPKey(namespace string) KeyFunc {
	return func(r *http.Request) string { return "rl:" + namespace + ":" + ClientIP(r) }
}

// The 429 body is written inline to keep this package free of an httpx import
// (httpx imports middleware; importing back would cycle).
const rateLimitedBody = `{"error":{"code":"rate_limited","message":"Too many requests."}}`

// RateLimit enforces `limit` requests per `window` per key. On limiter
// infrastructure errors it FAILS OPEN (serves the request) rather than blocking
// legitimate traffic on a Redis blip — availability over strictness for a soft
// control. Hard money controls never fail open (see ledger/limits, Stage 4).
func RateLimit(l Limiter, limit int, window time.Duration, key KeyFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, retryAfter, err := l.Allow(r.Context(), key(r), limit, window)
			if err != nil {
				next.ServeHTTP(w, r) // fail open on infra error
				return
			}
			if !ok {
				secs := int(retryAfter.Seconds())
				if secs < 1 {
					secs = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(rateLimitedBody))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
