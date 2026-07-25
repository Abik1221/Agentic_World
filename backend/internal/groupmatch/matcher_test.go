package groupmatch

import (
	"context"
	"errors"
	"io"
	"log/slog"
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

// fakeCreator records the groups it was asked to start.
type fakeCreator struct {
	seats   int
	err     error
	created [][]Seat
	calls   int
}

func (c *fakeCreator) SeatTarget() int { return c.seats }
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

// Enqueue rejects a game that has no group table creator (e.g. goofspiel).
func TestEnqueueRejectsUngroupedGame(t *testing.T) {
	clk := fixedClock{t: time.Unix(1_700_000_000, 0)}
	svc := testSvc(newMemRepo(clk), map[string]TableCreator{"mafia": &fakeCreator{seats: 12}}, fakeRating{}, clk)
	_, err := svc.Enqueue(context.Background(), "a", "o", "goofspiel", 100)
	if err != ErrGameNotGrouped {
		t.Fatalf("goofspiel should be rejected as not-grouped, got %v", err)
	}
}
