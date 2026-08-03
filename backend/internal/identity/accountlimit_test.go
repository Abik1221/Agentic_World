package identity

import (
	"context"
	"testing"
	"time"
)

// The per-account throttle exists for the attack a per-IP limit cannot see: the same
// password list spread one guess per host across a botnet never trips any single IP
// bucket. These tests pin the behaviour of the handler-side gate.
func TestAllowAccount(t *testing.T) {
	t.Run("nil limiter allows everything (unchanged default)", func(t *testing.T) {
		h := &Handler{}
		if !h.allowAccount(context.Background(), "someone@example.com") {
			t.Fatal("a handler with no account limiter must not block logins")
		}
	})

	t.Run("refusal is propagated", func(t *testing.T) {
		h := &Handler{}
		h.SetAccountRateLimit(func(context.Context, string) (bool, time.Duration) {
			return false, time.Minute
		})
		if h.allowAccount(context.Background(), "victim@example.com") {
			t.Fatal("an exhausted budget must refuse the attempt")
		}
	})

	// Casing and whitespace must not buy a second budget for the same account, or the
	// limit is trivially bypassed by sending Victim@Example.com instead.
	t.Run("identifier is normalised before keying", func(t *testing.T) {
		var seen []string
		h := &Handler{}
		h.SetAccountRateLimit(func(_ context.Context, id string) (bool, time.Duration) {
			seen = append(seen, id)
			return true, 0
		})
		for _, v := range []string{"Victim@Example.com", "  victim@example.com  ", "VICTIM@EXAMPLE.COM"} {
			h.allowAccount(context.Background(), v)
		}
		for i, got := range seen {
			if got != "victim@example.com" {
				t.Fatalf("variant %d keyed as %q; all casings/spacings must share one budget", i, got)
			}
		}
	})

	// An empty identifier must not consume budget. The service rejects a blank email
	// anyway, and spending a slot on it would let anyone drain a shared bucket by posting
	// nothing at all.
	t.Run("empty identifier does not consume budget", func(t *testing.T) {
		called := 0
		h := &Handler{}
		h.SetAccountRateLimit(func(context.Context, string) (bool, time.Duration) {
			called++
			return false, time.Minute
		})
		if !h.allowAccount(context.Background(), "   ") {
			t.Fatal("a blank identifier must be allowed through to the service's own validation")
		}
		if called != 0 {
			t.Fatalf("limiter consulted %d times for a blank identifier; want 0", called)
		}
	})
}
