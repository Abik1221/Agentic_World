package payout

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeTotals struct {
	current, prior int64
	err            error
	calls          int
}

func (f *fakeTotals) NetPaidBetween(_ context.Context, from, to time.Time) (int64, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	// The first call is the current window; the second is the baseline span.
	if f.calls == 1 {
		return f.current, nil
	}
	return f.prior, nil
}

func now() time.Time { return time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC) }

// The request that would BREACH the ceiling is the one refused, not the one after it.
// Counting only settled volume would let the breach itself through every time.
func TestPendingPayoutCountsTowardTheCeiling(t *testing.T) {
	b := &Breaker{WindowCents: 100_000, Window: time.Hour}
	tot := &fakeTotals{current: 95_000}

	if err := b.Check(context.Background(), tot, now(), 4_000); err != nil {
		t.Fatalf("99,000 of a 100,000 ceiling should pass: %v", err)
	}
	b.Reset()
	tot.calls = 0
	if err := b.Check(context.Background(), tot, now(), 6_000); err == nil {
		t.Fatal("101,000 of a 100,000 ceiling was allowed")
	}
}

// Growth defeats a fixed cap: an attack no longer looks large in absolute terms once
// the platform has grown into the ceiling set months earlier.
func TestSpikeTripsEvenWellUnderTheAbsoluteCeiling(t *testing.T) {
	b := &Breaker{
		WindowCents:   10_000_000, // deliberately generous
		Window:        time.Hour,
		SpikeMultiple: 4, BaselineWindows: 24, MinBaselineCents: 1_000,
	}
	// 24h of prior volume = 240,000 → a 10,000/hour baseline.
	tot := &fakeTotals{current: 45_000, prior: 240_000}

	err := b.Check(context.Background(), tot, now(), 1_000)
	if err == nil {
		t.Fatal("46,000 against a 10,000 baseline is 4.6x and should have tripped")
	}
	var open ErrBreakerOpen
	if !errors.As(err, &open) {
		t.Fatalf("want ErrBreakerOpen, got %T", err)
	}
}

// A quiet period must not make every ordinary withdrawal look like an infinite spike.
// Without the floor the breaker trips every Monday on a perfectly healthy platform.
func TestBaselineFloorPreventsFalseTripsAfterAQuietPeriod(t *testing.T) {
	b := &Breaker{
		Window: time.Hour, SpikeMultiple: 4, BaselineWindows: 24,
		MinBaselineCents: 50_000,
	}
	tot := &fakeTotals{current: 0, prior: 0} // nothing paid out at all recently

	if err := b.Check(context.Background(), tot, now(), 100_000); err != nil {
		t.Fatalf("a normal payout after a quiet spell tripped the breaker: %v", err)
	}
}

// Once open it STAYS open. A breaker that heals itself is one an attacker waits out.
func TestStaysOpenUntilResetByAHuman(t *testing.T) {
	b := &Breaker{WindowCents: 1_000, Window: time.Hour}
	tot := &fakeTotals{current: 5_000}

	if err := b.Check(context.Background(), tot, now(), 1); err == nil {
		t.Fatal("expected the ceiling to trip")
	}
	// Volume back to nothing, an hour later — still refused.
	quiet := &fakeTotals{current: 0}
	if err := b.Check(context.Background(), quiet, now().Add(time.Hour), 1); err == nil {
		t.Fatal("breaker healed itself; an attacker would simply wait")
	}
	b.Reset()
	if err := b.Check(context.Background(), &fakeTotals{current: 0}, now(), 1); err != nil {
		t.Fatalf("after an explicit reset payouts should resume: %v", err)
	}
}

// Not being able to SEE how much has left is not a reason to keep letting it leave.
func TestUnreadableVolumeTripsRatherThanPassing(t *testing.T) {
	b := &Breaker{WindowCents: 100_000, Window: time.Hour}
	tot := &fakeTotals{err: errors.New("database unavailable")}

	if err := b.Check(context.Background(), tot, now(), 1); err == nil {
		t.Fatal("a failed volume read allowed a payout through")
	}
	if open, _, _ := b.Tripped(); !open {
		t.Fatal("the breaker did not latch on an unreadable read")
	}
}

// The first reason is the one that describes what went wrong; later trips are
// consequences of it and must not overwrite the diagnosis.
func TestFirstReasonIsKept(t *testing.T) {
	b := &Breaker{}
	b.Trip("ceiling reached", now())
	b.Trip("something later", now().Add(time.Minute))
	_, reason, _ := b.Tripped()
	if reason != "ceiling reached" {
		t.Fatalf("reason = %q, want the original diagnosis", reason)
	}
}

// Unconfigured means no opinion — it must not block a platform that has not set it up.
func TestUnconfiguredBreakerAllows(t *testing.T) {
	if err := (&Breaker{}).Check(context.Background(), &fakeTotals{}, now(), 1_000_000); err != nil {
		t.Fatalf("an unconfigured breaker blocked a payout: %v", err)
	}
}
