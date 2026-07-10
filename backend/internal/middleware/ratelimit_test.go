package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// erroringLimiter always fails, simulating a Redis outage.
type erroringLimiter struct{}

func (erroringLimiter) Allow(context.Context, string, int, time.Duration) (bool, time.Duration, error) {
	return false, 0, errors.New("redis down")
}

func serve(mw func(http.Handler) http.Handler) int {
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	return rec.Code
}

// SEC-M2: when the primary limiter errors, RateLimitFailover must enforce via the
// local fallback (fail closed after the limit), not serve unthrottled.
func TestRateLimitFailoverUsesLocalFallbackOnPrimaryError(t *testing.T) {
	local := NewLocalLimiter()
	key := func(*http.Request) string { return "rl:login:1.2.3.4" }
	mw := RateLimitFailover(erroringLimiter{}, local, 2, time.Minute, key)

	if c := serve(mw); c != http.StatusOK {
		t.Fatalf("req 1 = %d, want 200", c)
	}
	if c := serve(mw); c != http.StatusOK {
		t.Fatalf("req 2 = %d, want 200", c)
	}
	// Third within the window: the local fallback denies (does NOT fail open).
	if c := serve(mw); c != http.StatusTooManyRequests {
		t.Fatalf("req 3 = %d, want 429 (fallback must not fail open)", c)
	}
}

func TestLocalLimiterFixedWindow(t *testing.T) {
	l := NewLocalLimiter()
	ctx := context.Background()
	ok, _, _ := l.Allow(ctx, "k", 1, time.Minute)
	if !ok {
		t.Fatal("first call must be allowed")
	}
	ok, retry, _ := l.Allow(ctx, "k", 1, time.Minute)
	if ok || retry <= 0 {
		t.Fatalf("second call over limit must be denied with a retry-after, got ok=%v retry=%v", ok, retry)
	}
	// A different key is independent.
	if ok, _, _ := l.Allow(ctx, "other", 1, time.Minute); !ok {
		t.Fatal("independent key must be allowed")
	}
}
