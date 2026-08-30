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

	"bytes"
	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/exploit"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/readycheck"
	"reflect"
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
	advances      []advanceCall     // every Advance, in order — see the round-start guard
	// lastCreate is the most recent CreateWaitingMatch input, so a test can assert on
	// fields match.Match does not carry back — Private in particular.
	lastCreate match.CreateMatchInput
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{matches: map[string]match.Match{}, events: map[string][]gs.Event{}}
}

func (r *fakeRepo) CreateWaitingMatch(_ context.Context, in match.CreateMatchInput) (match.Match, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastCreate = in
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

func (r *fakeRepo) Activate(_ context.Context, id string, joiner match.Player, state gs.State, startsAt, deadline time.Time, events []gs.Event) error {
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
	sa := startsAt
	m.StartsAt = &sa
	r.matches[id] = m
	r.events[id] = append(r.events[id], events...)
	return nil
}

func (r *fakeRepo) CreatePairedActive(_ context.Context, in match.CreatePairedInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := in.Deadline
	m := match.Match{
		PublicID: in.PublicID, Game: in.Game, Status: match.StatusActive,
		Mode: in.Mode, BotPolicy: in.BotPolicy, Bid: in.Bid,
		RakePct: in.RakePct, TotalRounds: in.TotalRounds, EngineVersion: in.EngineVersion,
		Commit: in.Commit, FairnessMode: in.FairnessMode, Seed: in.Seed,
		Players: []match.Player{in.SeatA, in.SeatB}, State: in.State, RoundDeadline: &d,
	}
	// Mirror the store: a zero StartsAt stays nil rather than becoming year 1.
	if !in.StartsAt.IsZero() {
		sa := in.StartsAt
		m.StartsAt = &sa
	}
	r.matches[in.PublicID] = m
	r.events[in.PublicID] = append(r.events[in.PublicID], in.Events...)
	return nil
}

// advanceCall records one Advance for the round-start guard below. roundStarted is the
// field that matters: it must be nil on every write that does not open a new round.
type advanceCall struct {
	deadline     *time.Time
	roundStarted *time.Time
}

func (r *fakeRepo) Advance(_ context.Context, id string, state gs.State, deadline *time.Time, roundStarted *time.Time, events []gs.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advances = append(r.advances, advanceCall{deadline: deadline, roundStarted: roundStarted})
	m := r.matches[id]
	if m.Status != match.StatusActive {
		return match.ErrNotActive
	}
	m.State = state
	m.RoundDeadline = deadline
	if roundStarted != nil {
		m.RoundStartedAt = roundStarted
	}
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
	//
	// Past the COUNTDOWN as well as the window: since Join sets a start countdown (like mafia
	// and monopoly), the first round's deadline is startsAt+window, not now+window. Advancing
	// only the window leaves the deadline in the future and the sweep correctly finds nothing —
	// which used to read as "the sweeper is broken" rather than "the clock has not reached it".
	clk.advance(readycheck.DefaultPolicy("goofspiel").Countdown + 21*time.Second)
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
	// Past the start countdown as well as the window — see TestSweepForcesTimeout.
	clk.advance(readycheck.DefaultPolicy("goofspiel").Countdown + 21*time.Second)

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

// ExtendDeadline satisfies match.Repo. Records the push so a test can assert an extension
// happened without needing a database.
func (f *fakeRepo) ExtendDeadline(_ context.Context, matchPublicID string, deadline time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.matches[matchPublicID]; ok {
		d := deadline
		m.RoundDeadline = &d
		f.matches[matchPublicID] = m
	}
	return nil
}

// TestRoundStartIsStampedOnlyWhenARoundOpens is the guard for a bug the database caught
// after the unit tests and a mutation check had both passed.
//
// commit() calls Advance THREE times for different reasons: one seat sealed (same round),
// the round finished (same round), and the next round opened. The round-start column was
// stamped with now() unconditionally inside the Advance SQL, so the first seat's seal — and
// every chat message, because trySay commits too — reset the origin that think-time is
// measured from. Observed live: round_started_at moved three times inside one round while
// the round number and deadline stayed fixed, and recorded think-times collapsed from the
// real 6-10s to 19-915ms.
//
// That number feeds verification.Record, which decides whether a HUMAN is playing by hand.
// Near-zero response times are the strongest possible "not a human" signal, so the
// corruption ran straight into a fraud control.
//
// Asserted on the CALLS rather than on the stored value: what went wrong was which writes
// carried a timestamp, and only the call log shows that.
func TestRoundStartIsStampedOnlyWhenARoundOpens(t *testing.T) {
	svc, repo := newSvcWithRepo()
	ctx := context.Background()

	id, err := svc.CreateOpen(ctx, "ag_a", "usr_a", 50)
	if err != nil {
		t.Fatalf("CreateOpen: %v", err)
	}
	if _, err := svc.Join(ctx, "ag_b", "usr_b", id); err != nil {
		t.Fatalf("Join: %v", err)
	}

	// Seal ONE seat, then speak. Neither opens a round, so neither may carry a start.
	first, _ := svc.State(ctx, id, "ag_a", false, 0)
	if len(first.You.Hand) == 0 {
		t.Fatal("no hand dealt")
	}
	repo.mu.Lock()
	repo.advances = nil
	repo.mu.Unlock()

	if _, err := svc.Act(ctx, "ag_a", id, first.Round, first.You.Hand[0], ""); err != nil {
		t.Fatalf("Act(seat a): %v", err)
	}
	// Speaking is the case that made this obvious in production: an agent that talks
	// during a round must not move the round's origin.
	_, _ = svc.Say(ctx, "ag_a", id, "thinking out loud", "table")

	repo.mu.Lock()
	sameRound := append([]advanceCall(nil), repo.advances...)
	repo.mu.Unlock()

	if len(sameRound) == 0 {
		t.Fatal("no Advance recorded for a seal — the guard is testing nothing")
	}
	for i, c := range sameRound {
		if c.roundStarted != nil {
			t.Errorf("same-round Advance #%d stamped a round start (%v) — the origin moves mid-round",
				i, c.roundStarted)
		}
	}

	// Now complete the round. Exactly one Advance may carry a start, and it must equal the
	// moment the round opened — not the deadline, which is that moment plus the window.
	repo.mu.Lock()
	repo.advances = nil
	repo.mu.Unlock()

	second, _ := svc.State(ctx, id, "ag_b", false, 0)
	if _, err := svc.Act(ctx, "ag_b", id, second.Round, second.You.Hand[0], ""); err != nil {
		t.Fatalf("Act(seat b): %v", err)
	}

	repo.mu.Lock()
	opened := append([]advanceCall(nil), repo.advances...)
	repo.mu.Unlock()

	stamped := 0
	for _, c := range opened {
		if c.roundStarted == nil {
			continue
		}
		stamped++
		if c.deadline != nil && !c.roundStarted.Before(*c.deadline) {
			t.Errorf("round start %v is not before the deadline %v — the start was set to the "+
				"deadline instead of the moment the round opened", c.roundStarted, c.deadline)
		}
	}
	if stamped != 1 {
		t.Errorf("%d Advance calls stamped a round start when the round resolved; want exactly 1", stamped)
	}
}

// AgentKind answers "external" for anything these tests seat. They exercise the DEVELOPER
// paths, and a stub that claimed "harness" would let a zero-stake benchmark table be created
// in tests that are not about it — hiding the very check CreateHarnessPaired exists for.
// harnessKinds lets a test opt specific agents into the platform-benchmark kind. Empty by
// default so the developer-path tests are unaffected.
var harnessKinds = map[string]bool{}

func (f *fakeRepo) AgentKind(_ context.Context, id string) (string, error) {
	if harnessKinds[id] {
		return "harness", nil
	}
	return "external", nil
}

// TestJoinSetsStartCountdown pins that a goofspiel lobby table gets the same start countdown
// mafia and monopoly already persist.
//
// This was a real, measurable gap rather than a theoretical one: across three days of lab
// traffic every mafia row had a starts_at and every goofspiel row had NULL, because Join
// activated the table in the same instant it seated the joiner. Two consequences, and the
// second is the one that cost agents rounds:
//
//   - no surface had an absolute instant to count to, so no countdown could be rendered;
//   - the first round's deadline was measured from the join, so an agent still starting up
//     was already losing its first window.
//
// The assertions are on RELATIONSHIPS (starts_at strictly after the join, deadline strictly
// after starts_at), not on the literal 10s, so retuning the policy does not fail this test
// while removing the countdown still does.
func TestJoinSetsStartCountdown(t *testing.T) {
	svc, repo := newSvcWithRepo()
	ctx := context.Background()
	id, _ := svc.CreateOpen(ctx, "ag_a", "usr_a", 50)
	if _, err := svc.Join(ctx, "ag_b", "usr_b", id); err != nil {
		t.Fatal(err)
	}

	repo.mu.Lock()
	m := repo.matches[id]
	repo.mu.Unlock()

	// The service runs on a fixed clock, so "now" is that instant, not wall time.
	now := time.Unix(1_700_000_000, 0).UTC()
	if m.StartsAt == nil {
		t.Fatal("joined table has no starts_at — the start countdown was not set, so no surface " +
			"can count to a shared instant (this is exactly the goofspiel gap)")
	}
	if !m.StartsAt.After(now) {
		t.Fatalf("starts_at %v is not after the join instant %v — a countdown that has already "+
			"elapsed is the same as no countdown", m.StartsAt, now)
	}
	if m.RoundDeadline == nil || !m.RoundDeadline.After(*m.StartsAt) {
		t.Fatalf("round deadline %v must be strictly after starts_at %v, otherwise the countdown "+
			"eats the first round's thinking time", m.RoundDeadline, m.StartsAt)
	}
}

// TestHarnessBoardSeedIsDeterministicAndShared pins the property duplicate scheduling needs:
// the same board seed must produce the same deal for every pairing, and a different seed must
// produce a different one.
//
// If this ever stops holding, two pairings are no longer playing the same board and the
// within-board comparison silently becomes an ordinary unpaired one — which is the failure mode
// that made the 2026-08-20 run unreadable, and it would not announce itself.
func TestHarnessBoardSeedIsDeterministicAndShared(t *testing.T) {
	ctx := context.Background()
	for _, id := range []string{"ag_h1", "ag_h2", "ag_h3", "ag_h4", "ag_h5", "ag_h6"} {
		harnessKinds[id] = true
		defer delete(harnessKinds, id)
	}
	seedA := exploit.BoardSeed("spec-v1", 3)
	seedB := exploit.BoardSeed("spec-v1", 3)
	seedOther := exploit.BoardSeed("spec-v1", 4)

	if !bytes.Equal(seedA, seedB) {
		t.Fatal("BoardSeed is not deterministic; two pairings would play different deals on the " +
			"same board index")
	}
	if bytes.Equal(seedA, seedOther) {
		t.Fatal("two different boards derived the same seed; the board set collapses to one deal")
	}
	if len(seedA) != 32 {
		t.Fatalf("BoardSeed returned %d bytes, want 32 — the service rejects any other length",
			len(seedA))
	}

	// Two separate harness tables handed the same seed must deal the same prize order.
	svc, repo := newSvcWithRepo()
	id1, err := svc.CreateHarnessPaired(ctx, "ag_h1", "usr_sys", "ag_h2", "usr_sys", seedA)
	if err != nil {
		t.Fatalf("first harness table: %v", err)
	}
	id2, err := svc.CreateHarnessPaired(ctx, "ag_h3", "usr_sys", "ag_h4", "usr_sys", seedB)
	if err != nil {
		t.Fatalf("second harness table: %v", err)
	}

	repo.mu.Lock()
	m1, m2 := repo.matches[id1], repo.matches[id2]
	repo.mu.Unlock()

	if !reflect.DeepEqual(m1.State.PrizeOrder, m2.State.PrizeOrder) {
		t.Fatalf("same seed produced different prize orders: %v vs %v",
			m1.State.PrizeOrder, m2.State.PrizeOrder)
	}

	// And a different board must actually differ, or blocking has nothing to block on.
	id3, err := svc.CreateHarnessPaired(ctx, "ag_h5", "usr_sys", "ag_h6", "usr_sys", seedOther)
	if err != nil {
		t.Fatalf("third harness table: %v", err)
	}
	repo.mu.Lock()
	m3 := repo.matches[id3]
	repo.mu.Unlock()
	if reflect.DeepEqual(m1.State.PrizeOrder, m3.State.PrizeOrder) {
		t.Fatal("two different boards dealt the same prize order; the board set is degenerate")
	}
}

// TestHarnessRejectsShortBoardSeed. A silently zero-extended seed would make two boards collide
// and the collision would look like a genuine result.
func TestHarnessRejectsShortBoardSeed(t *testing.T) {
	svc := newSvc()
	if _, err := svc.CreateHarnessPaired(context.Background(),
		"ag_h1", "usr_sys", "ag_h2", "usr_sys", []byte{1, 2, 3}); err == nil {
		t.Fatal("a 3-byte board seed was accepted; it must be rejected, not padded")
	}
}

// TestHarnessTableGetsStartCountdown closes the last path without one.
//
// Mafia, monopoly and the goofspiel lobby all persist starts_at; harness tables did not, and 6 of
// 6 finished benchmark tables carried NULL — so the console could render no countdown for the
// platform's own runs, which are precisely the ones an operator watches.
func TestHarnessTableGetsStartCountdown(t *testing.T) {
	ctx := context.Background()
	for _, id := range []string{"ag_hc1", "ag_hc2"} {
		harnessKinds[id] = true
		defer delete(harnessKinds, id)
	}
	svc, repo := newSvcWithRepo()
	id, err := svc.CreateHarnessPaired(ctx, "ag_hc1", "usr_sys", "ag_hc2", "usr_sys", nil)
	if err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	m := repo.matches[id]
	repo.mu.Unlock()

	now := time.Unix(1_700_000_000, 0).UTC()
	if m.StartsAt == nil {
		t.Fatal("a harness table was created with no starts_at — no surface can count to a " +
			"shared instant for the platform's own benchmark runs")
	}
	if !m.StartsAt.After(now) {
		t.Fatalf("starts_at %v is not after the creation instant %v; a countdown that has "+
			"already elapsed is the same as none", m.StartsAt, now)
	}
	if m.RoundDeadline == nil || !m.RoundDeadline.After(*m.StartsAt) {
		t.Fatalf("round deadline %v must follow starts_at %v, or the countdown eats the first "+
			"round's thinking time", m.RoundDeadline, m.StartsAt)
	}
}
