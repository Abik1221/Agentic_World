package middleware

import (
	"context"
	"net/http"
	"strconv"
	"sync"
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

// RateLimitFailover is RateLimit for buckets that must NOT fail open (auth:
// login/register/token-exchange). When the primary limiter errors (e.g. a Redis
// blip) it consults an in-process fallback instead of serving the request, so a
// brute-force window doesn't open during a cache outage. It only truly fails open
// if the fallback also errors (the local limiter never does).
func RateLimitFailover(primary, fallback Limiter, limit int, window time.Duration, key KeyFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k := key(r)
			ok, retryAfter, err := primary.Allow(r.Context(), k, limit, window)
			if err != nil {
				// Fail to the local per-instance limiter, not open.
				ok, retryAfter, err = fallback.Allow(r.Context(), k, limit, window)
				if err != nil {
					next.ServeHTTP(w, r) // last-resort: local limiter never errors in practice
					return
				}
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

// LocalLimiter is an in-process fixed-window Limiter used as the fail-closed
// fallback for auth buckets when the shared (Redis) limiter is unavailable. It
// bounds abuse per instance (not globally) — enough to blunt brute-force during a
// cache outage without taking auth down. Safe for concurrent use.
type LocalLimiter struct {
	mu   sync.Mutex
	hits map[string]*localWindow
}

type localWindow struct {
	count   int
	resetAt time.Time
}

// NewLocalLimiter builds an empty in-process limiter.
func NewLocalLimiter() *LocalLimiter { return &LocalLimiter{hits: map[string]*localWindow{}} }

// Allow implements Limiter with a fixed window per key. Expired windows are reset
// lazily on access; a crude size cap prevents unbounded growth under a flood of
// distinct keys during an outage.
func (l *LocalLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > 100_000 {
		l.hits = map[string]*localWindow{} // bound memory; worst case briefly under-limits
	}
	e := l.hits[key]
	if e == nil || now.After(e.resetAt) {
		l.hits[key] = &localWindow{count: 1, resetAt: now.Add(window)}
		return true, 0, nil
	}
	if e.count >= limit {
		return false, e.resetAt.Sub(now), nil
	}
	e.count++
	return true, 0, nil
}
