package match

import (
	"context"
	"errors"
	"testing"
	"time"
)

type flakyRater struct {
	failsLeft int
	calls     int
}

func (r *flakyRater) Rate(context.Context, RatingResult) error {
	r.calls++
	if r.failsLeft > 0 {
		r.failsLeft--
		return errors.New("transient")
	}
	return nil
}

// TestRateWithRetry guards M3: a transient rater error right after the match
// commits is retried (rating is idempotent), so the leaderboard update isn't lost.
func TestRateWithRetry(t *testing.T) {
	// Fails twice, succeeds on the 3rd attempt.
	r := &flakyRater{failsLeft: 2}
	if err := rateWithRetry(context.Background(), r, RatingResult{}, 3, 0); err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if r.calls != 3 {
		t.Fatalf("Rate called %d times, want 3", r.calls)
	}

	// Exhausts all attempts -> surfaces the error (not silently dropped).
	r2 := &flakyRater{failsLeft: 5}
	if err := rateWithRetry(context.Background(), r2, RatingResult{}, 3, 0); err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
	if r2.calls != 3 {
		t.Fatalf("Rate called %d times, want 3 (bounded)", r2.calls)
	}
}

// A cancelled context stops the retry loop promptly.
func TestRateWithRetryHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &flakyRater{failsLeft: 1}
	// Large backoff so the cancelled ctx (not the timer) wins the retry gate.
	if err := rateWithRetry(ctx, r, RatingResult{}, 3, 10*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
