package match_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/platform"
)

// ── in-memory fakes (no DB / Redis) ──────────────────────────────────────────

type fakeRepo struct {
	mu            sync.Mutex
	matches       map[string]match.Match
	events        map[string][]gs.Event
	finishedEvent []byte // last match.finished payload passed to Finish (nil = none)
	finishCalls   int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{matches: map[string]match.Match{}, events: map[string][]gs.Event{}}
}

func (r *fakeRepo) CreateWaitingMatch(_ context.Context, in match.CreateMatchInput) (match.Match, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := match.Match{
		PublicID: in.PublicID, Game: in.Game, Status: match.StatusWaiting, Bid: in.Bid,
		RakePct: in.RakePct, TotalRounds: in.TotalRounds, EngineVersion: in.EngineVersion,
		Commit: in.Commit, FairnessMode: in.FairnessMode, Seed: in.Seed,
		Players: []match.Player{in.Creator},
	}
	r.matches[in.PublicID] = m
	return m, nil
}

func (r *fakeRepo) ListWaiting(_ context.Context, game string, bid int64, exclude string, _ int) ([]match.LobbyItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []match.LobbyItem
	for _, m := range r.matches {
		if m.Status == match.StatusWaiting && m.Game == game && (bid <= 0 || m.Bid == bid) && m.Players[0].OwnerPublicID != exclude {
			out = append(out, match.LobbyItem{PublicID: m.PublicID, Game: m.Game, Bid: m.Bid, CreatorAgentPublicID: m.Players[0].AgentPublicID})
		}
	}
	return out, nil
}

func (r *fakeRepo) Get(_ context.Context, id string) (match.Match, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.matches[id]
	if !ok {
		return match.Match{}, match.ErrNotFound
	}
	return m, nil
}

func (r *fakeRepo) Activate(_ context.Context, id string, joiner match.Player, state gs.State, deadline time.Time, events []gs.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.matches[id]
	if m.Status != match.StatusWaiting {
		return match.ErrNotWaiting
	}
	m.Status = match.StatusActive
	m.Players = append(m.Players, joiner)
	m.State = state
	d := deadline
	m.RoundDeadline = &d
	r.matches[id] = m
	r.events[id] = append(r.events[id], events...)
	return nil
}

func (r *fakeRepo) CreatePairedActive(_ context.Context, in match.CreatePairedInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := in.Deadline
	r.matches[in.PublicID] = match.Match{
		PublicID: in.PublicID, Game: in.Game, Status: match.StatusActive,
		Mode: in.Mode, BotPolicy: in.BotPolicy, Bid: in.Bid,
		RakePct: in.RakePct, TotalRounds: in.TotalRounds, EngineVersion: in.EngineVersion,
		Commit: in.Commit, FairnessMode: in.FairnessMode, Seed: in.Seed,
		Players: []match.Player{in.SeatA, in.SeatB}, State: in.State, RoundDeadline: &d,
	}
	r.events[in.PublicID] = append(r.events[in.PublicID], in.Events...)
	return nil
}

func (r *fakeRepo) Advance(_ context.Context, id string, state gs.State, deadline *time.Time, events []gs.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.matches[id]
	if m.Status != match.StatusActive {
		return match.ErrNotActive
	}
	m.State = state
	m.RoundDeadline = deadline
	r.matches[id] = m
	r.events[id] = append(r.events[id], events...)
	return nil
}

func (r *fakeRepo) Finish(_ context.Context, id string, state gs.State, winner, hash string, players []match.Player, events []gs.Event, finishedEvent []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.matches[id]
	m.Status = match.StatusFinished
	m.State = state
	m.WinnerAgent = winner
	m.ReplayHash = hash
	m.Players = players
	m.RoundDeadline = nil
	r.matches[id] = m
	r.events[id] = append(r.events[id], events...)
	r.finishedEvent = finishedEvent // captured for the match.finished emission assertion
	r.finishCalls++
	return nil
}

func (r *fakeRepo) ListActiveExpired(_ context.Context, now time.Time, _ int) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for id, m := range r.matches {
		if m.Status == match.StatusActive && m.RoundDeadline != nil && !now.Before(*m.RoundDeadline) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (r *fakeRepo) LoadEvents(_ context.Context, id string) ([]gs.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]gs.Event(nil), r.events[id]...), nil
}

func (r *fakeRepo) AgentSigningKey(context.Context, string) (string, error) { return "", nil }
func (r *fakeRepo) RecordMoveSignature(context.Context, string, int, int, int, string, string) error {
	return nil
}
func (r *fakeRepo) LoadMoveSignatures(context.Context, string) ([]match.MoveSignature, error) {
	return nil, nil
}
func (r *fakeRepo) CancelWaiting(_ context.Context, matchPublicID, creatorAgentPublicID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.matches[matchPublicID]
	if !ok || m.Status != match.StatusWaiting {
		return match.ErrNotWaiting
	}
	if len(m.Players) == 0 || m.Players[0].AgentPublicID != creatorAgentPublicID {
		return match.ErrNotCreator
	}
	m.Status = match.StatusAborted
	r.matches[matchPublicID] = m
	return nil
}

type fakeLocker struct{}

func (fakeLocker) Lock(context.Context, string, time.Duration) (func(), bool, error) {
	return func() {}, true, nil
}

func newSvc() *match.Service {
	svc, _ := newSvcWithRepo()
	return svc
}

// newSvcWithRepo exposes the fake repo so tests can assert on captured writes
// (e.g. the transactional match.finished payload).
func newSvcWithRepo() (*match.Service, *fakeRepo) {
	repo := newFakeRepo()
	svc := match.New(repo, fakeLocker{}, match.NoopLimits{}, match.NoopWallet{},
		match.NoopBroadcaster{}, match.AllowAllVerifier{}, match.NoopRater{}, match.NoopFinishHook{},
		platform.FixedClock{T: time.Unix(1_700_000_000, 0).UTC()},
		match.Config{MoveWindow: 20 * time.Second, RakePct: 5, Rounds: 13, LockTTL: 5 * time.Second})
	return svc, repo
}

// TestCompetitiveMatchEmitsFinishedEvent proves a competitive match hands a
// non-nil match.finished payload (carrying the winner) to repo.Finish — the
// transactional-outbox emission that drives the first-win badge + notifications.
func TestCompetitiveMatchEmitsFinishedEvent(t *testing.T) {
	svc, repo := newSvcWithRepo()
	ctx := context.Background()

	id, err := svc.CreateOpen(ctx, "ag_a", "usr_a", 50)
	if err != nil {
		t.Fatalf("CreateOpen: %v", err)
	}
	if _, err := svc.Join(ctx, "ag_b", "usr_b", id); err != nil {
		t.Fatalf("Join: %v", err)
	}
	for i := 0; i < 100; i++ {
		v, _ := svc.State(ctx, id, "ag_a", false, 0)
		if v.Status == match.StatusFinished {
			break
		}
		for _, ag := range []string{"ag_a", "ag_b"} {
			cur, _ := svc.State(ctx, id, ag, false, 0)
			if cur.YourTurn && len(cur.You.Hand) > 0 {
				_, _ = svc.Act(ctx, ag, id, cur.Round, cur.You.Hand[0], "")
			}
		}
	}

	if repo.finishCalls == 0 {
		t.Fatal("Finish was never called")
	}
	if repo.finishedEvent == nil {
		t.Fatal("competitive match must emit a match.finished payload")
	}
	var p struct {
		MatchID     string `json:"match_id"`
		Game        string `json:"game"`
		WinnerAgent string `json:"winner_agent"`
	}
	if err := json.Unmarshal(repo.finishedEvent, &p); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if p.MatchID != id || p.Game == "" {
		t.Fatalf("unexpected payload: %+v", p)
	}
	// Winner is one of the two seats (deterministic play rarely ties here).
	if p.WinnerAgent != "ag_a" && p.WinnerAgent != "ag_b" && p.WinnerAgent != "" {
		t.Fatalf("winner should be a seat or empty (tie), got %q", p.WinnerAgent)
	}
}

// ── tests ────────────────────────────────────────────────────────────────────

func TestMatchHappyPathToFinish(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	id, err := svc.CreateOpen(ctx, "ag_a", "usr_a", 50)
	if err != nil {
		t.Fatalf("CreateOpen: %v", err)
	}
	jv, err := svc.Join(ctx, "ag_b", "usr_b", id)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if jv.Status != match.StatusActive || jv.Round != 1 {
		t.Fatalf("after join: status=%s round=%d", jv.Status, jv.Round)
	}

	// Drive both agents to completion (each plays its lowest legal card).
	for i := 0; i < 100; i++ {
		v, _ := svc.State(ctx, id, "ag_a", false, 0)
		if v.Status == match.StatusFinished {
			break
		}
		for _, ag := range []string{"ag_a", "ag_b"} {
			cur, _ := svc.State(ctx, id, ag, false, 0)
			if cur.YourTurn && len(cur.You.Hand) > 0 {
				if _, err := svc.Act(ctx, ag, id, cur.Round, cur.You.Hand[0], ""); err != nil {
					t.Fatalf("Act(%s) round %d: %v", ag, cur.Round, err)
				}
			}
		}
	}

	final, _ := svc.State(ctx, id, "ag_a", false, 0)
	if final.Status != match.StatusFinished {
		t.Fatalf("match did not finish: status=%s round=%d", final.Status, final.Round)
	}
	if final.Result == nil {
		t.Fatal("finished match has no result")
	}
	if len(final.History) != 13 {
		t.Fatalf("history len = %d, want 13", len(final.History))
	}
}

func TestSameOwnerCannotJoin(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	id, _ := svc.CreateOpen(ctx, "ag_a", "usr_a", 50)
	if _, err := svc.Join(ctx, "ag_a2", "usr_a", id); err != match.ErrSameOwner {
		t.Fatalf("join by same owner = %v, want ErrSameOwner", err)
	}
}

func TestActionIdempotentAndGuards(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	id, _ := svc.CreateOpen(ctx, "ag_a", "usr_a", 50)
	if _, err := svc.Join(ctx, "ag_b", "usr_b", id); err != nil {
		t.Fatal(err)
	}

	a, _ := svc.State(ctx, id, "ag_a", false, 0)
	card := a.You.Hand[0]
	if _, err := svc.Act(ctx, "ag_a", id, a.Round, card, ""); err != nil {
		t.Fatalf("first act: %v", err)
	}
	// Idempotent: re-acting the same round is a no-op, not an error.
	if _, err := svc.Act(ctx, "ag_a", id, a.Round, card, ""); err != nil {
		t.Fatalf("idempotent re-act: %v", err)
	}
	// Wrong round is rejected.
	if _, err := svc.Act(ctx, "ag_a", id, a.Round+5, card, ""); err != match.ErrWrongRound {
		t.Fatalf("wrong round = %v, want ErrWrongRound", err)
	}
	// A non-player cannot act.
	if _, err := svc.Act(ctx, "ag_x", id, a.Round, 1, ""); err != match.ErrNotPlayer {
		t.Fatalf("non-player act = %v, want ErrNotPlayer", err)
	}
}

func TestSweepForcesTimeout(t *testing.T) {
	// Use a clock we can advance past the deadline, then sweep.
	repo := newFakeRepo()
	clk := &mutableClock{t: time.Unix(1_700_000_000, 0).UTC()}
	svc := match.New(repo, fakeLocker{}, match.NoopLimits{}, match.NoopWallet{},
		match.NoopBroadcaster{}, match.AllowAllVerifier{}, match.NoopRater{}, match.NoopFinishHook{}, clk,
		match.Config{MoveWindow: 20 * time.Second, RakePct: 5, Rounds: 13, LockTTL: 5 * time.Second})
	ctx := context.Background()

	id, _ := svc.CreateOpen(ctx, "ag_a", "usr_a", 50)
	if _, err := svc.Join(ctx, "ag_b", "usr_b", id); err != nil {
		t.Fatal(err)
	}
	// Neither agent acts; advance past the move window and sweep.
	clk.advance(21 * time.Second)
	n, err := svc.SweepExpired(ctx, 10)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n == 0 {
		t.Fatal("sweep processed 0 expired matches")
	}
	// Round 1 should have resolved via forced timeouts → round advanced.
	v, _ := svc.State(ctx, id, "ag_a", false, 0)
	if len(v.History) < 1 {
		t.Fatalf("expected at least one resolved round after timeout sweep, got %d", len(v.History))
	}
}

type mutableClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *mutableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
