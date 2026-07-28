// Package liveness tells a PLATFORM outage apart from an agent quitting, so the
// recovery sweep cannot confiscate stakes for turns nobody could have played.
//
// # Why this is needed
//
// A missed turn forfeits a staked match: the engine plays a deterministic fallback
// move for the absent seat, the table finishes, and that seat loses on merit with its
// stake going to the winner. That is exactly right when an agent is killed or crashes.
//
// The problem is that our own downtime looks identical on the wire. If the service is
// down for five minutes, every in-flight match blows its deadline, and the sweep that
// runs on recovery force-timeouts them all at once — silently taking stakes from
// players who were online and willing the whole time.
//
// # Why grace, and not automatic void-and-refund
//
// The obvious design is "detect a mass disconnect, void those matches, refund". It is
// the wrong one, and deliberately not what this does.
//
// Detecting an outage by counting simultaneous agent disconnects is a signal AGENTS
// CONTROL. Anyone running several agents could disconnect them together to trip the
// heuristic and void matches they were losing — turning the refund path into a
// free "undo" for bad games. Any rule where a player's own behaviour can trigger a
// refund of their stake is exploitable by construction.
//
// So detection here keys on something agents cannot cause: whether WE were serving.
// Instances heartbeat into one row; a gap proves nobody was up. And the remedy is the
// mildest one that fixes the injustice — extend the deadline so the match continues
// and is decided on play. No coins move, so there is nothing to game: an agent that
// was genuinely gone still forfeits, just on a fair clock.
//
// Voiding and refunding a match remains an OPERATOR action for a confirmed incident,
// with an audit trail. A human decides; this code never guesses with money.
package liveness

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Repo persists the heartbeat and outage records.
type Repo interface {
	// LastBeat returns the most recent heartbeat, or false when the row is absent.
	LastBeat(ctx context.Context) (time.Time, bool, error)
	// Beat records that this instance is serving, at `at`.
	Beat(ctx context.Context, at time.Time) error
	// RecordOutage appends an audit row for a detected gap.
	RecordOutage(ctx context.Context, startedAt, detectedAt, graceUntil time.Time, gapSeconds int64) error
	// ExtendActiveDeadlines pushes every ACTIVE match whose move deadline already
	// lapsed out to `until`, returning how many were moved. All three games share the
	// matches table, so one statement covers the platform.
	ExtendActiveDeadlines(ctx context.Context, until time.Time) (int64, error)
}

// Clock is the time source (injectable so the grace logic is testable).
type Clock interface{ Now() time.Time }

const (
	// BeatEvery is how often a serving instance refreshes the heartbeat.
	BeatEvery = 15 * time.Second
	// MinGap is how long a heartbeat gap must be before we call it an outage.
	// Comfortably above BeatEvery so an ordinary deploy or a slow write is not
	// mistaken for downtime.
	MinGap = 90 * time.Second
	// GraceAfter is how long forfeits stay suppressed once we are back. It must
	// exceed a full move window so every seat gets a real chance to act, not just
	// a technically-open deadline it could never have met.
	GraceAfter = 3 * time.Minute
	// deadlineHeadroom is added past the grace window when re-arming lapsed deadlines,
	// so a match is not instantly expired again the moment grace ends. It must exceed
	// the longest phase/move window in any game.
	deadlineHeadroom = 2 * time.Minute
	// MaxGrace caps the window however long the outage was: an operator should
	// handle a multi-hour incident deliberately, not have the sweeper paused for
	// hours by a heuristic.
	MaxGrace = 15 * time.Minute
)

// Tracker answers "is the platform inside a post-outage grace window?".
//
// Safe to use before Detect runs and safe when nil-repo: it simply reports no
// grace, i.e. the existing forfeit behaviour. Failing that way round matters —
// a bug here must not become a blanket amnesty on every match.
type Tracker struct {
	clock Clock
	log   *slog.Logger

	mu         sync.RWMutex
	graceUntil time.Time
}

func NewTracker(clock Clock, log *slog.Logger) *Tracker {
	return &Tracker{clock: clock, log: log}
}

// InGrace reports whether forfeits should be suppressed right now.
func (t *Tracker) InGrace() bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	until := t.graceUntil
	t.mu.RUnlock()
	return !until.IsZero() && t.clock.Now().Before(until)
}

// GraceUntil is the current grace deadline (zero when not in grace).
func (t *Tracker) GraceUntil() time.Time {
	if t == nil {
		return time.Time{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.graceUntil
}

// Detect reads the previous heartbeat and opens a grace window if there was a gap.
// Call once at boot, BEFORE the first Beat, or the gap is erased.
func (t *Tracker) Detect(ctx context.Context, repo Repo) {
	if t == nil || repo == nil {
		return
	}
	prev, ok, err := repo.LastBeat(ctx)
	if err != nil || !ok {
		// No history (fresh install) or an unreadable row: assume no outage. Opening
		// a grace window on an error would suppress every legitimate forfeit.
		return
	}
	now := t.clock.Now()
	gap := now.Sub(prev)
	if gap < MinGap {
		return // an ordinary restart, not an outage
	}
	grace := GraceAfter
	if gap > MaxGrace {
		grace = MaxGrace
	}
	until := now.Add(grace)

	t.mu.Lock()
	t.graceUntil = until
	t.mu.Unlock()

	// Suppressing the sweep is not enough on its own: the deadlines LAPSED during the
	// outage, so once grace expires ListActiveExpired returns exactly the same matches
	// and forfeits them all — the outcome this whole mechanism exists to prevent. Push
	// them past the window so every seat gets a real move window after recovery.
	//
	// Deliberately only moves deadlines ALREADY in the past: a match whose clock is
	// still running was never wronged and must keep its original deadline.
	//
	// Best-effort — a failed extension must not stop grace from opening, since a
	// suppressed sweep is still strictly better than an immediate mass forfeit.
	if moved, err := repo.ExtendActiveDeadlines(ctx, until.Add(deadlineHeadroom)); err != nil {
		if t.log != nil {
			t.log.Warn("could not extend lapsed match deadlines after outage", "err", err)
		}
	} else if t.log != nil && moved > 0 {
		t.log.Warn("extended lapsed match deadlines after outage", "matches", moved)
	}

	if t.log != nil {
		t.log.Warn("platform outage detected on boot; suppressing match forfeits",
			"gap", gap.String(), "last_beat", prev, "grace_until", until)
	}
	if err := repo.RecordOutage(ctx, prev, now, until, int64(gap.Seconds())); err != nil && t.log != nil {
		t.log.Warn("outage audit row not written", "err", err)
	}
}

// Run heartbeats until the context is cancelled. Best-effort: a failed write is
// logged and retried on the next tick — it must never take the server down.
func (t *Tracker) Run(ctx context.Context, repo Repo) {
	if t == nil || repo == nil {
		<-ctx.Done()
		return
	}
	tick := time.NewTicker(BeatEvery)
	defer tick.Stop()
	if err := repo.Beat(ctx, t.clock.Now()); err != nil && t.log != nil {
		t.log.Warn("liveness beat failed", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := repo.Beat(ctx, t.clock.Now()); err != nil && t.log != nil {
				t.log.Warn("liveness beat failed", "err", err)
			}
		}
	}
}
