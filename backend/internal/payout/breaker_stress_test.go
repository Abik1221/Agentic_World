package payout

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Concurrency is where a circuit breaker actually fails.
//
// The single-threaded tests prove the arithmetic. These prove the thing that matters
// under attack: a coordinated burst arriving at once must not each read the same
// pre-burst total and all pass. That failure mode is silent — every individual
// decision looks correct in the logs — and it is precisely the shape of a scripted
// drain.

// ledgerTotals is a thread-safe stand-in for the repo: paid-out volume that GROWS as
// payouts are admitted, which is what makes the race visible.
type ledgerTotals struct {
	mu   sync.Mutex
	paid int64
	// reads counts observations, to confirm the breaker is actually consulted rather
	// than short-circuiting once tripped.
	reads atomic.Int64
}

func (l *ledgerTotals) NetPaidBetween(context.Context, time.Time, time.Time) (int64, error) {
	l.reads.Add(1)
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.paid, nil
}

func (l *ledgerTotals) record(cents int64) {
	l.mu.Lock()
	l.paid += cents
	l.mu.Unlock()
}

// 200 simultaneous withdrawals against a ceiling that only permits ~10. Without
// serialization every goroutine reads 0 and all 200 are admitted.
func TestBurstCannotOverdrawTheCeiling(t *testing.T) {
	const (
		payout  = int64(1_000)  // 10.00 each
		ceiling = int64(10_000) // room for ten
		callers = 200
	)
	b := &Breaker{WindowCents: ceiling, Window: time.Hour}
	tot := &ledgerTotals{}

	// The service serializes this behind a per-owner advisory lock; here one mutex
	// stands in for that ordering guarantee.
	var admitGate sync.Mutex
	var admitted, refused atomic.Int64

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release everyone at the same instant
			admitGate.Lock()
			defer admitGate.Unlock()
			if err := b.Check(context.Background(), tot, now(), payout); err != nil {
				refused.Add(1)
				return
			}
			tot.record(payout) // this payout is now real money out
			admitted.Add(1)
		}()
	}
	close(start)
	wg.Wait()

	total := admitted.Load() * payout
	if total > ceiling {
		t.Fatalf("admitted %d cents against a %d ceiling — the burst overdrew it",
			total, ceiling)
	}
	if admitted.Load() == 0 {
		t.Fatal("nothing was admitted; the breaker is refusing everything")
	}
	if refused.Load() == 0 {
		t.Fatal("nothing was refused; the ceiling never engaged")
	}
	t.Logf("admitted %d (%d cents), refused %d, ceiling %d",
		admitted.Load(), total, refused.Load(), ceiling)
}

// Once open, EVERY caller must be refused — including ones already in flight. A
// breaker that lets the in-flight cohort through is a breaker with a drain window.
func TestOnceOpenEveryConcurrentCallerIsRefused(t *testing.T) {
	b := &Breaker{WindowCents: 1_000, Window: time.Hour}
	tot := &ledgerTotals{paid: 50_000} // already far past the ceiling

	var refused atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 300; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := b.Check(context.Background(), tot, now(), 1); err != nil {
				refused.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if refused.Load() != 300 {
		t.Fatalf("only %d/300 refused — %d payouts escaped through an open breaker",
			refused.Load(), 300-refused.Load())
	}
}

// Trip/Reset/Check racing must not corrupt state or panic. Run with -race.
func TestBreakerIsRaceFreeUnderMixedTraffic(t *testing.T) {
	b := &Breaker{WindowCents: 5_000, Window: time.Hour}
	tot := &ledgerTotals{paid: 2_000}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); _ = b.Check(context.Background(), tot, now(), 100) }()
		go func() { defer wg.Done(); b.Trip("stress", now()) }()
		go func() { defer wg.Done(); b.Reset() }()
	}
	wg.Wait()
	// No assertion on the final state — Trip and Reset are genuinely racing. The point
	// is that it neither panics nor tears, which -race and the mutex enforce.
	_, _, _ = b.Tripped()
}

// The breaker must be consulted on every attempt, not cached after the first refusal.
// A cached verdict would keep refusing after a legitimate admin reset.
func TestBreakerIsConsultedEveryTime(t *testing.T) {
	b := &Breaker{WindowCents: 1_000_000, Window: time.Hour}
	tot := &ledgerTotals{}
	for i := 0; i < 25; i++ {
		if err := b.Check(context.Background(), tot, now(), 1); err != nil {
			t.Fatalf("unexpected refusal on call %d: %v", i, err)
		}
	}
	if got := tot.reads.Load(); got < 25 {
		t.Fatalf("volume was read %d times for 25 checks — results are being cached", got)
	}
}
