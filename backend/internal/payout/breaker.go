package payout

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// The payout circuit breaker: the backstop that decides the platform survives a bad
// night.
//
// Every other control here is PER-OWNER — velocity caps, cooldowns, clearing. They
// bound what one account can move, and they are the right shape for a stolen session.
// None of them sees the shape that actually causes insolvency: many accounts each
// withdrawing a perfectly legal amount at the same time. A leaked signing key, a bug
// that credits coins, a coordinated ring of real accounts — in every case each
// individual withdrawal passes every per-owner check, and the treasury empties anyway.
//
// This watches the ONE number that matters, total money leaving per unit time, and
// stops everything when it moves in a way the platform has not seen before.
//
// TWO INDEPENDENT TRIGGERS, because each is blind to what the other catches:
//
//   - ABSOLUTE ceiling. Predictable and easy to reason about, and the only thing that
//     works on day one when there is no history to compare against.
//   - RELATIVE spike, versus a trailing baseline. Catches the case where the absolute
//     cap was set generously months ago and the platform has since grown into it, so
//     an attack no longer looks large in absolute terms.
//
// It FAILS CLOSED and STAYS closed. Once tripped, payouts halt until a human resets
// it. That is deliberate: a breaker that resets itself on a timer is a breaker that an
// attacker simply waits out, and the cost of a false trip is a support ticket while
// the cost of a missed one is the treasury.
type Breaker struct {
	// WindowCents is the ceiling on total net payout in Window. 0 disables this
	// trigger, which is only sensible if the relative trigger is configured.
	WindowCents int64
	// Window is the period the ceiling applies to.
	Window time.Duration
	// SpikeMultiple trips when the current window exceeds this multiple of the
	// trailing baseline. 0 disables. 3–5 is the usual band: below 3 trips on ordinary
	// weekly variance, above 5 stops catching anything before real damage.
	SpikeMultiple float64
	// BaselineWindows is how many prior windows form the baseline average. More
	// windows is steadier but slower to reflect genuine growth.
	BaselineWindows int
	// MinBaselineCents is the floor under the baseline. Without it, a quiet period
	// makes the baseline near-zero and ANY normal withdrawal reads as an infinite
	// spike — the breaker would trip every Monday morning on a healthy platform.
	MinBaselineCents int64

	mu        sync.RWMutex
	trippedAt time.Time
	reason    string
}

// PayoutTotals reports net payout cents in a period. Implemented by the payout repo.
type PayoutTotals interface {
	NetPaidBetween(ctx context.Context, from, to time.Time) (int64, error)
}

// ErrBreakerOpen is returned while payouts are halted.
type ErrBreakerOpen struct{ Reason string }

func (e ErrBreakerOpen) Error() string {
	return "payouts are temporarily halted: " + e.Reason
}

// Tripped reports whether payouts are currently halted, and why.
func (b *Breaker) Tripped() (bool, string, time.Time) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return !b.trippedAt.IsZero(), b.reason, b.trippedAt
}

// Trip halts payouts. Safe to call repeatedly; the FIRST reason is kept, because that
// is the one describing what actually went wrong — later calls are consequences.
func (b *Breaker) Trip(reason string, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.trippedAt.IsZero() {
		b.trippedAt = at
		b.reason = reason
	}
}

// Reset resumes payouts. Deliberately manual: see the note on the type.
func (b *Breaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.trippedAt = time.Time{}
	b.reason = ""
}

// Check evaluates the breaker and returns ErrBreakerOpen when payouts must not
// proceed. `pending` is the payout about to be made, counted toward the window so the
// request that would BREACH the ceiling is the one refused — not the one after it.
//
// A repo error trips the breaker rather than being ignored. Not being able to see how
// much money has left is not a reason to keep letting money leave.
func (b *Breaker) Check(ctx context.Context, totals PayoutTotals, now time.Time, pendingCents int64) error {
	if open, reason, _ := b.Tripped(); open {
		return ErrBreakerOpen{Reason: reason}
	}
	if b.Window <= 0 || totals == nil {
		return nil // unconfigured: no opinion
	}

	current, err := totals.NetPaidBetween(ctx, now.Add(-b.Window), now)
	if err != nil {
		b.Trip("could not read recent payout volume", now)
		return ErrBreakerOpen{Reason: "could not read recent payout volume"}
	}
	projected := current + pendingCents

	if b.WindowCents > 0 && projected > b.WindowCents {
		reason := fmt.Sprintf("payout ceiling reached (%d of %d cents in %s)",
			projected, b.WindowCents, b.Window)
		b.Trip(reason, now)
		return ErrBreakerOpen{Reason: reason}
	}

	if b.SpikeMultiple > 0 && b.BaselineWindows > 0 {
		start := now.Add(-b.Window * time.Duration(b.BaselineWindows+1))
		end := now.Add(-b.Window)
		prior, err := totals.NetPaidBetween(ctx, start, end)
		if err != nil {
			b.Trip("could not read the payout baseline", now)
			return ErrBreakerOpen{Reason: "could not read the payout baseline"}
		}
		baseline := prior / int64(b.BaselineWindows)
		if baseline < b.MinBaselineCents {
			baseline = b.MinBaselineCents
		}
		if baseline > 0 && float64(projected) > float64(baseline)*b.SpikeMultiple {
			reason := fmt.Sprintf("payout volume spiked (%d cents vs a %d baseline, over %.1fx)",
				projected, baseline, b.SpikeMultiple)
			b.Trip(reason, now)
			return ErrBreakerOpen{Reason: reason}
		}
	}
	return nil
}
