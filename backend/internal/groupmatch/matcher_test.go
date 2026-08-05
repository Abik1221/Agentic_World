package groupmatch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"
)

// ── fakes ─────────────────────────────────────────────────────────────────────

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// memRepo is an in-memory Repo modelling the waiting->claimed->matched machine.
type memRepo struct {
	entries map[string]*Entry
	clk     clock
}

func newMemRepo(clk clock) *memRepo { return &memRepo{entries: map[string]*Entry{}, clk: clk} }

func (r *memRepo) Upsert(_ context.Context, e Entry) error {
	e.Status = StatusWaiting
	e.MatchID = ""
	e.EnqueuedAt = r.clk.Now()
	cp := e
	r.entries[e.AgentPublicID] = &cp
	return nil
}
func (r *memRepo) Get(_ context.Context, agent string) (Entry, error) {
	e, ok := r.entries[agent]
	if !ok {
		return Entry{}, ErrNotQueued
	}
	return *e, nil
}
func (r *memRepo) Delete(_ context.Context, agent string) error { delete(r.entries, agent); return nil }
func (r *memRepo) WaitingByGame(_ context.Context, game string, _ int) ([]Entry, error) {
	var out []Entry
	for _, e := range r.entries {
		if e.Game == game && e.Status == StatusWaiting {
			out = append(out, *e)
		}
	}
	// Deterministic order: by enqueued_at, then agent id (stable pools in tests).
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].EnqueuedAt.Before(out[i].EnqueuedAt) ||
				(out[j].EnqueuedAt.Equal(out[i].EnqueuedAt) && out[j].AgentPublicID < out[i].AgentPublicID) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}
func (r *memRepo) ClaimGroup(_ context.Context, agents []string) (bool, error) {
	for _, a := range agents {
		e, ok := r.entries[a]
		if !ok || e.Status != StatusWaiting {
			return false, nil
		}
	}
	for _, a := range agents {
		r.entries[a].Status = "claimed"
	}
	return true, nil
}
func (r *memRepo) ReleaseGroup(_ context.Context, agents []string) error {
	for _, a := range agents {
		if e, ok := r.entries[a]; ok && e.Status == "claimed" {
			e.Status = StatusWaiting
		}
	}
	return nil
}
func (r *memRepo) MarkMatchedGroup(_ context.Context, agents []string, matchID string) error {
	for _, a := range agents {
		if e, ok := r.entries[a]; ok {
			e.Status = StatusMatched
			e.MatchID = matchID
		}
	}
	return nil
}
func (r *memRepo) PoolStats(_ context.Context, game string, bid int64, agent string) (PoolStats, error) {
	var ps PoolStats
	owners := map[string]bool{}
	subject, waiting := r.entries[agent], []*Entry{}
	for _, e := range r.entries {
		if e.Game == game && e.Bid == bid && e.Status == StatusWaiting {
			waiting = append(waiting, e)
			owners[e.OwnerPublicID] = true
		}
	}
	ps.Waiting, ps.DistinctOwners = len(waiting), len(owners)
	if subject == nil || subject.Status != StatusWaiting {
		return ps, nil // not waiting ⇒ no position, matching the SQL behaviour
	}
	for _, e := range waiting {
		if e.EnqueuedAt.Before(subject.EnqueuedAt) ||
			(e.EnqueuedAt.Equal(subject.EnqueuedAt) && e.AgentPublicID <= subject.AgentPublicID) {
			ps.Position++
		}
	}
	return ps, nil
}

// fakeCreator records the groups it was asked to start. min defaults to seats (i.e.
// full-roster-only) unless a test sets it.
type fakeCreator struct {
	seats   int
	min     int
	err     error
	created [][]Seat
	calls   int
}

func (c *fakeCreator) SeatTarget() int { return c.seats }
func (c *fakeCreator) MinSeats() int {
	if c.min == 0 {
		return c.seats
	}
	return c.min
}
func (c *fakeCreator) CreateStartedTable(_ context.Context, seats []Seat, _ int64) (string, error) {
	c.calls++
	if c.err != nil {
		return "", c.err
	}
	c.created = append(c.created, seats)
	return "match-1", nil
}

type fakeRating struct{ elos map[string]int }

func (f fakeRating) Elo(_ context.Context, agent, _ string) (int, error) {
	if f.elos == nil {
		return 1500, nil
	}
	return f.elos[agent], nil
}

func testSvc(repo Repo, creators map[string]TableCreator, rating RatingSource, clk clock) *Service {
	return New(repo, creators, rating, clk, Config{Interval: time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

func enqueue(t *testing.T, s *Service, agent, owner, game string, bid int64) {
	t.Helper()
	if _, err := s.Enqueue(context.Background(), agent, owner, game, bid); err != nil {
		t.Fatalf("enqueue %s: %v", agent, err)
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

// A full group of distinct-owner agents forms a started table, and all are matched.
func TestFormsFullGroup(t *testing.T) {
	clk := fixedClock{t: time.Unix(1_700_000_000, 0)}
	repo := newMemRepo(clk)
	creator := &fakeCreator{seats: 2}
	svc := testSvc(repo, map[string]TableCreator{"monopoly": creator}, fakeRating{}, clk)

	enqueue(t, svc, "a", "o-a", "monopoly", 100)
	enqueue(t, svc, "b", "o-b", "monopoly", 100)
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if creator.calls != 1 || len(creator.created) != 1 || len(creator.created[0]) != 2 {
		t.Fatalf("expected one 2-seat table, got calls=%d created=%v", creator.calls, creator.created)
	}
	for _, ag := range []string{"a", "b"} {
		if e, _ := svc.Status(context.Background(), ag); e.Status != StatusMatched || e.MatchID != "match-1" {
			t.Fatalf("%s should be matched into match-1, got %+v", ag, e)
		}
	}
}

// Below the seat target, no table forms and everyone keeps waiting.
func TestWaitsUntilTargetMet(t *testing.T) {
	clk := fixedClock{t: time.Unix(1_700_000_000, 0)}
	repo := newMemRepo(clk)
	creator := &fakeCreator{seats: 3}
	svc := testSvc(repo, map[string]TableCreator{"mafia": creator}, fakeRating{}, clk)

	enqueue(t, svc, "a", "o-a", "mafia", 50)
	enqueue(t, svc, "b", "o-b", "mafia", 50)
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if creator.calls != 0 {
		t.Fatalf("must not form a table below target (3), got %d calls", creator.calls)
	}
	for _, ag := range []string{"a", "b"} {
		if e, _ := svc.Status(context.Background(), ag); e.Status != StatusWaiting {
			t.Fatalf("%s should still be waiting, got %q", ag, e.Status)
		}
	}
}

// Two agents of the SAME owner never seat together; a table forms only once enough
// DISTINCT owners are present.
func TestDistinctOwnerOnly(t *testing.T) {
	clk := fixedClock{t: time.Unix(1_700_000_000, 0)}
	repo := newMemRepo(clk)
	creator := &fakeCreator{seats: 2}
	svc := testSvc(repo, map[string]TableCreator{"monopoly": creator}, fakeRating{}, clk)

	enqueue(t, svc, "a1", "same", "monopoly", 100)
	enqueue(t, svc, "a2", "same", "monopoly", 100)
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if creator.calls != 0 {
		t.Fatalf("same-owner agents must not seat together, got %d calls", creator.calls)
	}
	enqueue(t, svc, "b1", "other", "monopoly", 100)
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick2: %v", err)
	}
	if creator.calls != 1 {
		t.Fatalf("a distinct-owner pair should now form, got %d calls", creator.calls)
	}
	// The formed group must contain exactly one of {a1,a2} plus b1 (distinct owners).
	seatOwners := map[string]bool{}
	for _, s := range creator.created[0] {
		seatOwners[s.OwnerPublicID] = true
	}
	if !seatOwners["same"] || !seatOwners["other"] || len(creator.created[0]) != 2 {
		t.Fatalf("group must be one 'same' + 'other', got %v", creator.created[0])
	}
}

// If table creation fails (e.g. a member can't join), the claim is released so all
// return to waiting — no agent is stranded 'claimed', and no table is recorded.
func TestReleaseOnCreateFailure(t *testing.T) {
	clk := fixedClock{t: time.Unix(1_700_000_000, 0)}
	repo := newMemRepo(clk)
	creator := &fakeCreator{seats: 2, err: errors.New("a member went broke")}
	svc := testSvc(repo, map[string]TableCreator{"monopoly": creator}, fakeRating{}, clk)

	enqueue(t, svc, "a", "o-a", "monopoly", 100)
	enqueue(t, svc, "b", "o-b", "monopoly", 100)
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if creator.calls != 1 {
		t.Fatalf("creation should have been attempted once, got %d", creator.calls)
	}
	for _, ag := range []string{"a", "b"} {
		if e, _ := svc.Status(context.Background(), ag); e.Status != StatusWaiting {
			t.Fatalf("%s should be back to waiting after a failed create, got %q", ag, e.Status)
		}
	}
}

// Ratings outside the band don't group at first, but the band widens with wait time
// until the long-waiting anchor can be matched.
func TestBandWidensWithWait(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	clk := &mutClock{t: start}
	repo := newMemRepo(clk)
	creator := &fakeCreator{seats: 2}
	// BaseBand 150, +150 every 10s. Elos 200 apart → excluded at t0, included after 10s.
	svc := New(repo, map[string]TableCreator{"monopoly": creator}, fakeRating{elos: map[string]int{"a": 1400, "b": 1600}},
		clk, Config{Interval: time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	enqueue(t, svc, "a", "o-a", "monopoly", 100)
	enqueue(t, svc, "b", "o-b", "monopoly", 100)
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick t0: %v", err)
	}
	if creator.calls != 0 {
		t.Fatalf("200-apart ratings must not group within the base band (150), got %d", creator.calls)
	}
	clk.t = start.Add(11 * time.Second) // band now 150 + 150 = 300 ≥ 200
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick t+11s: %v", err)
	}
	if creator.calls != 1 {
		t.Fatalf("after the band widened, the pair should group, got %d", creator.calls)
	}
}

type mutClock struct{ t time.Time }

func (c *mutClock) Now() time.Time { return c.t }

// A 12-agent Mafia roster forms exactly one table when the 12th distinct owner joins.
func TestMafiaFullRosterForms(t *testing.T) {
	clk := fixedClock{t: time.Unix(1_700_000_000, 0)}
	repo := newMemRepo(clk)
	creator := &fakeCreator{seats: 12}
	svc := testSvc(repo, map[string]TableCreator{"mafia": creator}, fakeRating{}, clk)

	ids := []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9", "m10", "m11", "m12"}
	for _, id := range ids {
		enqueue(t, svc, id, "o-"+id, "mafia", 500)
	}
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if creator.calls != 1 || len(creator.created[0]) != 12 {
		t.Fatalf("expected one 12-seat table, got calls=%d", creator.calls)
	}
}

// The blocker this package existed to hit: a pool that can never reach the seat target
// must eventually start anyway. Below MinSeats it still waits; at MinSeats it waits until
// ShortFormAfter has elapsed; then it forms with exactly the agents present.
func TestFormsShortHandedAfterWait(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	clk := &mutClock{t: start}
	repo := newMemRepo(clk)
	creator := &fakeCreator{seats: 12, min: 4}
	svc := New(repo, map[string]TableCreator{"mafia": creator}, fakeRating{}, clk,
		Config{Interval: time.Second, ShortFormAfter: 90 * time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	// Three agents is below MinSeats: no table, however long they wait.
	for _, id := range []string{"a", "b", "c"} {
		enqueue(t, svc, id, "o-"+id, "mafia", 500)
	}
	clk.t = start.Add(10 * time.Minute)
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick below min: %v", err)
	}
	if creator.calls != 0 {
		t.Fatalf("3 agents is below MinSeats(4) — must not form, got %d calls", creator.calls)
	}

	// A fourth arrives, reaching MinSeats. Its own wait is zero, but the ANCHOR has
	// waited well past ShortFormAfter, and the anchor's patience is what this spends.
	enqueue(t, svc, "d", "o-d", "mafia", 500)
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick at min: %v", err)
	}
	if creator.calls != 1 || len(creator.created) != 1 {
		t.Fatalf("expected one short table once MinSeats was reached, got calls=%d", creator.calls)
	}
	if got := len(creator.created[0]); got != 4 {
		t.Fatalf("short table should carry the 4 real agents, got %d seats", got)
	}
	for _, ag := range []string{"a", "b", "c", "d"} {
		if e, _ := svc.Status(context.Background(), ag); e.Status != StatusMatched {
			t.Fatalf("%s should be matched into the short table, got %q", ag, e.Status)
		}
	}
}

// Before ShortFormAfter elapses, a pool at MinSeats keeps waiting — a short-handed table
// is a fallback for a thin queue, not the default whenever a tick catches a partial pool.
func TestShortFormWaitsForTheWindow(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	clk := &mutClock{t: start}
	repo := newMemRepo(clk)
	creator := &fakeCreator{seats: 12, min: 4}
	svc := New(repo, map[string]TableCreator{"mafia": creator}, fakeRating{}, clk,
		Config{Interval: time.Second, ShortFormAfter: 90 * time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	for _, id := range []string{"a", "b", "c", "d"} {
		enqueue(t, svc, id, "o-"+id, "mafia", 500)
	}
	clk.t = start.Add(89 * time.Second)
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick inside window: %v", err)
	}
	if creator.calls != 0 {
		t.Fatalf("must hold out for a fuller table inside the window, got %d calls", creator.calls)
	}
	clk.t = start.Add(91 * time.Second)
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick past window: %v", err)
	}
	if creator.calls != 1 {
		t.Fatalf("should form once the window has passed, got %d calls", creator.calls)
	}
}

// A full roster still forms IMMEDIATELY when one is available — the short-form path must
// not delay or shrink a table that could be complete.
func TestFullRosterStillPreferredImmediately(t *testing.T) {
	clk := fixedClock{t: time.Unix(1_700_000_000, 0)}
	repo := newMemRepo(clk)
	creator := &fakeCreator{seats: 12, min: 4}
	svc := New(repo, map[string]TableCreator{"mafia": creator}, fakeRating{}, clk,
		Config{Interval: time.Second, ShortFormAfter: 90 * time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	for i := 1; i <= 12; i++ {
		id := "m" + strconv.Itoa(i)
		enqueue(t, svc, id, "o-"+id, "mafia", 500)
	}
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if creator.calls != 1 || len(creator.created[0]) != 12 {
		t.Fatalf("a full roster must form at once and at full size, got calls=%d", creator.calls)
	}
}

// A game that opts out (MinSeats == SeatTarget) never forms below its target however long
// the pool waits — this is what keeps Monopoly's behaviour unchanged. Asserted at
// target-1, the largest short group possible, so the test fails if the short-form path
// ever stops respecting the opt-out (a smaller pool would pass for the trivial reason
// that it lacks MinSeats agents).
func TestMinSeatsEqualToTargetNeverFormsShort(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	clk := &mutClock{t: start}
	repo := newMemRepo(clk)
	creator := &fakeCreator{seats: 4, min: 4}
	svc := New(repo, map[string]TableCreator{"monopoly": creator}, fakeRating{}, clk,
		Config{Interval: time.Second, ShortFormAfter: time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	// Three distinct owners for a 4-seat target: one short, and long past the window.
	for _, id := range []string{"a", "b", "c"} {
		enqueue(t, svc, id, "o-"+id, "monopoly", 100)
	}
	clk.t = start.Add(time.Hour)
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if creator.calls != 0 {
		t.Fatalf("MinSeats == SeatTarget must never form short, got %d calls", creator.calls)
	}

	// The fourth owner completes the roster: it forms immediately, at full size.
	enqueue(t, svc, "d", "o-d", "monopoly", 100)
	if err := svc.NewMatcher().tick(context.Background()); err != nil {
		t.Fatalf("tick after 4th: %v", err)
	}
	if creator.calls != 1 || len(creator.created[0]) != 4 {
		t.Fatalf("a complete roster should form at full size, got calls=%d", creator.calls)
	}
}

// Status explains the wait: position in the pool, how many DISTINCT owners are waiting
// (the real ceiling on one table), and how long until a short table may start.
func TestStatusReportsQueueVisibility(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	clk := &mutClock{t: start}
	repo := newMemRepo(clk)
	creator := &fakeCreator{seats: 12, min: 4}
	svc := New(repo, map[string]TableCreator{"mafia": creator}, fakeRating{}, clk,
		Config{Interval: time.Second, ShortFormAfter: 90 * time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	enqueue(t, svc, "first", "o-1", "mafia", 500)
	clk.t = start.Add(30 * time.Second)
	// Two agents of ONE owner: three waiting, but only two owners can ever be seated.
	enqueue(t, svc, "second", "o-2", "mafia", 500)
	enqueue(t, svc, "third", "o-2", "mafia", 500)

	st, err := svc.Status(context.Background(), "first")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Position != 1 {
		t.Errorf("the oldest waiter is position 1, got %d", st.Position)
	}
	if st.PoolSize != 3 {
		t.Errorf("pool size = %d, want 3", st.PoolSize)
	}
	if st.DistinctOwners != 2 {
		t.Errorf("distinct owners = %d, want 2 (o-2 holds two agents)", st.DistinctOwners)
	}
	if st.SeatsNeeded != 12 || st.MinSeats != 4 {
		t.Errorf("seat target/min = %d/%d, want 12/4", st.SeatsNeeded, st.MinSeats)
	}
	if st.WaitedMs != 30_000 {
		t.Errorf("waited = %dms, want 30000", st.WaitedMs)
	}
	// 90s window, 30s spent ⇒ 60s left before a short table is allowed.
	if st.ShortFormInMs != 60_000 {
		t.Errorf("short-form countdown = %dms, want 60000", st.ShortFormInMs)
	}

	// Past the window the countdown reads zero rather than going negative.
	clk.t = start.Add(2 * time.Minute)
	st, _ = svc.Status(context.Background(), "first")
	if st.ShortFormInMs != 0 {
		t.Errorf("countdown past the window = %dms, want 0", st.ShortFormInMs)
	}
}

// Enqueue rejects a game that has no group table creator (e.g. goofspiel).
func TestEnqueueRejectsUngroupedGame(t *testing.T) {
	clk := fixedClock{t: time.Unix(1_700_000_000, 0)}
	svc := testSvc(newMemRepo(clk), map[string]TableCreator{"mafia": &fakeCreator{seats: 12}}, fakeRating{}, clk)
	_, err := svc.Enqueue(context.Background(), "a", "o", "goofspiel", 100)
	if err != ErrGameNotGrouped {
		t.Fatalf("goofspiel should be rejected as not-grouped, got %v", err)
	}
}
