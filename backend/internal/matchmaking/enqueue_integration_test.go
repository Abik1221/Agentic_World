package matchmaking

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// fullRepo is an in-memory Repo that stores whole entries (not just status), so we
// can drive the REAL public flow end-to-end: Enqueue -> Waiting -> ClaimPair ->
// CreatePaired -> MarkMatched -> Status. The isolated matcher_test.go seeds status
// directly; this exercises the agent-facing API a new agent actually calls.
type fullRepo struct {
	clk     clock
	entries map[string]Entry
}

func newFullRepo(clk clock) *fullRepo {
	return &fullRepo{clk: clk, entries: map[string]Entry{}}
}

func (r *fullRepo) Upsert(_ context.Context, e Entry) error {
	e.Status = StatusWaiting
	e.MatchID = ""
	e.EnqueuedAt = r.clk.Now()
	r.entries[e.AgentPublicID] = e
	return nil
}

func (r *fullRepo) Get(_ context.Context, agent string) (Entry, error) {
	e, ok := r.entries[agent]
	if !ok {
		return Entry{}, ErrNotQueued
	}
	return e, nil
}

func (r *fullRepo) Delete(_ context.Context, agent string) error {
	delete(r.entries, agent)
	return nil
}

func (r *fullRepo) Waiting(_ context.Context, limit int) ([]Entry, error) {
	// Ordered by (bid, enqueued_at) like the real query; a stable insertion order is
	// fine for these tests since all bids/times are equal.
	var out []Entry
	for _, e := range r.entries {
		if e.Status == StatusWaiting {
			out = append(out, e)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *fullRepo) ClaimPair(_ context.Context, a, b string) (bool, error) {
	ea, oka := r.entries[a]
	eb, okb := r.entries[b]
	if !oka || !okb || ea.Status != StatusWaiting || eb.Status != StatusWaiting {
		return false, nil
	}
	ea.Status, eb.Status = "claimed", "claimed"
	r.entries[a], r.entries[b] = ea, eb
	return true, nil
}

func (r *fullRepo) ReleasePair(_ context.Context, a, b string) error {
	for _, x := range []string{a, b} {
		if e, ok := r.entries[x]; ok && e.Status == "claimed" {
			e.Status = StatusWaiting
			r.entries[x] = e
		}
	}
	return nil
}

func (r *fullRepo) MarkMatched(_ context.Context, a, b, matchID string) error {
	for _, x := range []string{a, b} {
		e := r.entries[x]
		e.Status = StatusMatched
		e.MatchID = matchID
		r.entries[x] = e
	}
	return nil
}

func newFullSvc(t *testing.T, repo Repo, pairer Pairer) *Service {
	t.Helper()
	return New(repo, pairer, fixedRating{}, systemClk{}, Config{Interval: time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)), prometheus.NewRegistry())
}

// A new agent enqueues via the public API; a matcher tick pairs it with another
// waiting agent of a different owner, escrows exactly once, and Status flips to
// matched carrying the match id. This is the "a new agent comes to play, how is it
// enqueued and matched" path, driven through Enqueue (not seeded status).
func TestEnqueueToMatched_FullFlow(t *testing.T) {
	repo := newFullRepo(systemClk{})
	pairer := &memPairer{}
	svc := newFullSvc(t, repo, pairer)
	ctx := context.Background()

	if _, err := svc.Enqueue(ctx, "ag_a", "owner-a", 100); err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	if _, err := svc.Enqueue(ctx, "ag_b", "owner-b", 100); err != nil {
		t.Fatalf("enqueue b: %v", err)
	}

	// Both should be waiting until the matcher runs.
	if e, _ := svc.Status(ctx, "ag_a"); e.Status != StatusWaiting {
		t.Fatalf("ag_a should be waiting pre-tick, got %q", e.Status)
	}

	if err := svc.NewMatcher().tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if pairer.calls != 1 {
		t.Fatalf("CreatePaired should escrow exactly once, got %d", pairer.calls)
	}
	for _, ag := range []string{"ag_a", "ag_b"} {
		e, err := svc.Status(ctx, ag)
		if err != nil {
			t.Fatalf("status %s: %v", ag, err)
		}
		if e.Status != StatusMatched {
			t.Fatalf("%s should be matched, got %q", ag, e.Status)
		}
		if e.MatchID != "match-1" {
			t.Fatalf("%s should carry the match id, got %q", ag, e.MatchID)
		}
	}
}

// Two agents of the SAME owner never get paired (self-play / collusion guard),
// even after a matcher tick — both stay waiting indefinitely. A new agent must be
// able to tell it's still waiting (not silently dropped).
func TestEnqueueSameOwnerNeverMatches(t *testing.T) {
	repo := newFullRepo(systemClk{})
	pairer := &memPairer{}
	svc := newFullSvc(t, repo, pairer)
	ctx := context.Background()

	_, _ = svc.Enqueue(ctx, "ag_a", "same-owner", 100)
	_, _ = svc.Enqueue(ctx, "ag_b", "same-owner", 100)

	if err := svc.NewMatcher().tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if pairer.calls != 0 {
		t.Fatalf("same-owner agents must never be paired, CreatePaired ran %d times", pairer.calls)
	}
	for _, ag := range []string{"ag_a", "ag_b"} {
		if e, _ := svc.Status(ctx, ag); e.Status != StatusWaiting {
			t.Fatalf("%s should still be waiting, got %q", ag, e.Status)
		}
	}
}

// stubElig is an Eligibility gate that rejects when err != nil. In production this
// gate also rejects an agent whose manifest doesn't declare the ranked game
// (Goofspiel) — the fix that keeps a Mafia/Monopoly-only agent out of the Goofspiel
// queue where it would forfeit-bleed its stake.
type stubElig struct {
	err   error
	calls int
}

func (s *stubElig) RequireCertified(context.Context, string) error {
	s.calls++
	return s.err
}

// An agent rejected by the eligibility gate (e.g. uncertified, suspended, or — the
// new case — its manifest doesn't declare the ranked game) never enters the queue.
func TestEnqueueRejectsIneligibleAgent(t *testing.T) {
	repo := &countingRepo{memRepo: newMemRepo()}
	svc := newEnqueueSvc(repo)
	want := ErrAgentOffline // any gate error; stands in for "ranked_game_unsupported"
	elig := &stubElig{err: want}
	svc.SetEligibility(elig)

	_, err := svc.Enqueue(context.Background(), "ag_mafia_only", "owner", 100)
	if err != want {
		t.Fatalf("ineligible agent should be rejected by the eligibility gate, got %v", err)
	}
	if elig.calls != 1 {
		t.Fatalf("eligibility should be checked once, got %d", elig.calls)
	}
	if repo.upserts != 0 {
		t.Fatalf("a rejected enqueue must not touch the queue, Upsert ran %d times", repo.upserts)
	}
}

// stubLive is a Liveness gate that rejects when err != nil.
type stubLive struct {
	err   error
	calls int
}

func (s *stubLive) RequireReachable(context.Context, string) error {
	s.calls++
	return s.err
}

// An offline agent (no live socket, no verified endpoint) is rejected at enqueue
// with ErrAgentOffline and never enters the queue — so it can never be matched and
// forfeit-bleed its stake. This is the fix for the auto-play-ranked money leak.
func TestEnqueueRejectsOfflineAgent(t *testing.T) {
	repo := &countingRepo{memRepo: newMemRepo()}
	svc := newEnqueueSvc(repo)
	live := &stubLive{err: ErrAgentOffline}
	svc.SetLiveness(live)

	_, err := svc.Enqueue(context.Background(), "ag_offline", "owner", 100)
	if err != ErrAgentOffline {
		t.Fatalf("offline agent should be rejected with ErrAgentOffline, got %v", err)
	}
	if live.calls != 1 {
		t.Fatalf("liveness should be checked once, got %d", live.calls)
	}
	if repo.upserts != 0 {
		t.Fatalf("a rejected (offline) enqueue must not touch the queue, Upsert ran %d times", repo.upserts)
	}
}

// A reachable agent passes the gate and enqueues normally; with no gate wired the
// behavior is unchanged (backward compatible).
func TestEnqueueAllowsReachableAgent(t *testing.T) {
	repo := &countingRepo{memRepo: newMemRepo()}
	svc := newEnqueueSvc(repo)
	live := &stubLive{} // reachable
	svc.SetLiveness(live)

	if _, err := svc.Enqueue(context.Background(), "ag_online", "owner", 100); err != nil {
		t.Fatalf("reachable agent should enqueue, got %v", err)
	}
	if live.calls != 1 || repo.upserts != 1 {
		t.Fatalf("expected 1 liveness check + 1 upsert, got checks=%d upserts=%d", live.calls, repo.upserts)
	}
}

// The "keep playing" loop: after a match finishes the queue entry is cleared, and
// the same agent can re-enqueue for another match — the round-trip that autoplay's
// reconciler performs continuously. Proves Enqueue is idempotent across matches and
// a finished agent is not stuck in a stale 'matched' entry when it comes back.
func TestReEnqueueAfterMatchClears(t *testing.T) {
	repo := newFullRepo(systemClk{})
	svc := newFullSvc(t, repo, &memPairer{})
	ctx := context.Background()

	_, _ = svc.Enqueue(ctx, "ag_a", "owner-a", 100)
	_, _ = svc.Enqueue(ctx, "ag_b", "owner-b", 100)
	if err := svc.NewMatcher().tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	// Match ended -> the finish path removes the queue entries.
	if err := svc.Cancel(ctx, "ag_a"); err != nil {
		t.Fatalf("cancel a: %v", err)
	}
	if _, err := svc.Status(ctx, "ag_a"); err != ErrNotQueued {
		t.Fatalf("cleared agent should be ErrNotQueued, got %v", err)
	}
	// Re-enter for the next match.
	if _, err := svc.Enqueue(ctx, "ag_a", "owner-a", 100); err != nil {
		t.Fatalf("re-enqueue a: %v", err)
	}
	e, err := svc.Status(ctx, "ag_a")
	if err != nil {
		t.Fatalf("status after re-enqueue: %v", err)
	}
	if e.Status != StatusWaiting || e.MatchID != "" {
		t.Fatalf("re-enqueued agent should be freshly waiting with no match id, got status=%q match=%q", e.Status, e.MatchID)
	}
}
