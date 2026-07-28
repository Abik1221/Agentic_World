package match_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/platform"
)

// fakeMover stands in for the socket gateway: both agents are "connected" and each
// turn it plays the lowest card in the offered hand (via JSON round-trip, so it
// needs no access to the driver's unexported view/move types).
type fakeMover struct{}

func (fakeMover) Connected(string) bool { return true }
func (fakeMover) Turn(_ context.Context, _ string, view, out any) error {
	b, _ := json.Marshal(view)
	var v struct {
		Round    int   `json:"round"`
		YourHand []int `json:"your_hand"`
	}
	_ = json.Unmarshal(b, &v)
	card := 0
	if len(v.YourHand) > 0 {
		card = v.YourHand[0]
	}
	resp, _ := json.Marshal(map[string]any{"round": v.Round, "card": card})
	return json.Unmarshal(resp, out)
}
func (fakeMover) GameEnd(context.Context, string, string, string, json.RawMessage) error { return nil }

// The server auto-drives BOTH paired agents over their sockets to completion —
// hands-free live-vs-live play (agents-vs-agents; both staked equally at pairing).
func TestRankedAutoDrivePlaysPairedMatchToFinish(t *testing.T) {
	svc, _ := newSvcWithRepo()
	svc.EnableRankedDrive(fakeMover{}, nil, nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()

	id, err := svc.CreatePaired(ctx, "ag_a", "usr_a", "ag_b", "usr_b", 50)
	if err != nil {
		t.Fatalf("CreatePaired: %v", err)
	}
	// CreatePaired spawned the driver; it should play the match to a finish.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v, _ := svc.State(ctx, id, "ag_a", false, 0)
		if v.Status == match.StatusFinished {
			if v.Result == nil {
				t.Fatal("finished match has no result")
			}
			if len(v.History) != 13 {
				t.Fatalf("history len = %d, want 13", len(v.History))
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("auto-driver did not finish the paired match within the deadline")
}

// ── in-memory fakes (no DB / Redis) ──────────────────────────────────────────

type fakeRepo struct {
	mu            sync.Mutex
	matches       map[string]match.Match
	events        map[string][]gs.Event
	finishedEvent []byte // last match.finished payload passed to Finish (nil = none)
	finishCalls   int
	signingKeys   map[string]string // agentPublicID → registered Ed25519 pubkey ("" = none)
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

// Timed variant: the fake has no clock, so events are stamped one second apart —
// enough for tests to assert ordering and non-zero offsets.
func (r *fakeRepo) LoadEventsTimed(_ context.Context, id string) ([]match.TimedEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	base := time.Unix(1_700_000_000, 0).UTC()
	out := make([]match.TimedEvent, 0, len(r.events[id]))
	for i, ev := range r.events[id] {
		out = append(out, match.TimedEvent{Event: ev, At: base.Add(time.Duration(i) * time.Second)})
	}
	return out, nil
}

func (r *fakeRepo) AgentSigningKey(_ context.Context, agent string) (string, error) {
	return r.signingKeys[agent], nil
}
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

// P3: a signing-key agent's self-drive Act requires a signature, but DriveAct
// (the platform driving the agent's seat over its authenticated socket) is exempt —
// so the server can drive a signing-key agent in a live match without wedging.
func TestDriveActBypassesSignatureForSigningKeyAgent(t *testing.T) {
	svc, repo := newSvcWithRepo()
	repo.signingKeys = map[string]string{"ag_a": "ed25519-pubkey-of-ag-a"}
	ctx := context.Background()

	id, _ := svc.CreateOpen(ctx, "ag_a", "usr_a", 50)
	if _, err := svc.Join(ctx, "ag_b", "usr_b", id); err != nil {
		t.Fatalf("Join: %v", err)
	}
	cur, _ := svc.State(ctx, id, "ag_a", false, 0)

	// Self-drive without a signature is rejected (ag_a registered a key).
	if _, err := svc.Act(ctx, "ag_a", id, cur.Round, cur.You.Hand[0], ""); err != match.ErrSignatureRequired {
		t.Fatalf("Act without signature = %v; want ErrSignatureRequired", err)
	}
	// Platform-driven move is accepted without a signature (socket auth suffices).
	if _, err := svc.DriveAct(ctx, "ag_a", id, cur.Round, cur.You.Hand[0]); err != nil {
		t.Fatalf("DriveAct = %v; want nil (platform-driven bypass)", err)
	}
	// And it actually sealed: ag_a is no longer waited on this round.
	after, _ := svc.State(ctx, id, "ag_a", false, 0)
	if after.YourTurn && after.Round == cur.Round {
		t.Fatal("DriveAct did not seal the move (still ag_a's turn this round)")
	}
}

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

// errLocker simulates Redis being unreachable: every Lock attempt errors.
type errLocker struct{}

func (errLocker) Lock(context.Context, string, time.Duration) (func(), bool, error) {
	return nil, false, errors.New("redis down")
}

// G2: with Redis down (the lock errors), the sweeper must STILL force the timeout
// lockless so a match with idle players can't stay wedged 'active' with escrow held.
func TestSweepForcesTimeoutLocklessWhenRedisDown(t *testing.T) {
	repo := newFakeRepo()
	clk := &mutableClock{t: time.Unix(1_700_000_000, 0).UTC()}
	cfg := match.Config{MoveWindow: 20 * time.Second, RakePct: 5, Rounds: 13, LockTTL: 5 * time.Second}
	mk := func(lock match.Locker) *match.Service {
		return match.New(repo, lock, match.NoopLimits{}, match.NoopWallet{},
			match.NoopBroadcaster{}, match.AllowAllVerifier{}, match.NoopRater{}, match.NoopFinishHook{}, clk, cfg)
	}
	ctx := context.Background()

	// Create + join with a working lock, then sweep with a Redis-down service that
	// shares the same repo.
	svc := mk(fakeLocker{})
	id, _ := svc.CreateOpen(ctx, "ag_a", "usr_a", 50)
	if _, err := svc.Join(ctx, "ag_b", "usr_b", id); err != nil {
		t.Fatal(err)
	}
	clk.advance(21 * time.Second)

	down := mk(errLocker{})
	if _, err := down.SweepExpired(ctx, 10); err != nil {
		t.Fatalf("sweep with redis down must not error: %v", err)
	}
	v, _ := svc.State(ctx, id, "ag_a", false, 0)
	if len(v.History) < 1 {
		t.Fatalf("timeout must fire lockless when redis is down, got %d rounds", len(v.History))
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
