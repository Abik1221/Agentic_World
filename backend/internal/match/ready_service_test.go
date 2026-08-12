package match_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/platform"
)

// These tests are about one sentence: NOTHING IS ESCROWED UNTIL EVERY SEAT IS READY.
//
// The state machine in internal/readycheck is already tested exhaustively. What is tested here
// is the part it cannot see — that the service takes money in exactly one situation and in
// exactly one order, and that every other way a ready-check table can end leaves the stakes
// alone. A bug here does not produce a wrong number on a board; it takes coins from someone
// who never got to play.

// ── doubles ──────────────────────────────────────────────────────────────────

type readyWallet struct {
	// Named apart from sandbox_test.go's spyWallet, which counts calls; this one records
	// WHICH match was staked and refunded, because the assertion here is about a specific
	// table's coins rather than a total. Embeds NoopWallet so only the two methods this
	// feature can reach are overridden — Settle and Refund belong to a match that has
	// already started, which by construction has not happened yet.
	match.NoopWallet
	mu       sync.Mutex
	staked   []string // match ids staked, in order
	refunded []string
	failNext bool
}

func (w *readyWallet) StakeMatch(_ context.Context, matchID, a, b string, bid int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failNext {
		return errStakeFailed
	}
	w.staked = append(w.staked, matchID)
	return nil
}

func (w *readyWallet) RefundStakes(_ context.Context, matchID, a, b string, bid int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refunded = append(w.refunded, matchID)
	return nil
}

func (w *readyWallet) stakes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.staked)
}

func (w *readyWallet) refunds() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.refunded)
}

type errString string

func (e errString) Error() string { return string(e) }

const errStakeFailed = errString("wallet: insufficient balance")

type fakeReadyRepo struct {
	seats      []match.ReadySeat
	activated  bool
	abandoned  bool
	activateOK bool
	asks       []string
	marked     []string
}

func (r *fakeReadyRepo) CreatePairedReadyCheck(_ context.Context, _ match.CreatePairedInput) error {
	return nil
}
func (r *fakeReadyRepo) MarkReady(_ context.Context, _, agent string, _ time.Time) (bool, error) {
	r.marked = append(r.marked, agent)
	return true, nil
}
func (r *fakeReadyRepo) ReadySeats(_ context.Context, _ string) ([]match.ReadySeat, error) {
	return r.seats, nil
}
func (r *fakeReadyRepo) RecordAsk(_ context.Context, _, agent string, _ time.Time) error {
	r.asks = append(r.asks, agent)
	return nil
}
func (r *fakeReadyRepo) ActivateAfterReady(_ context.Context, _ string, _, _ time.Time) (bool, error) {
	r.activated = true
	return r.activateOK, nil
}
func (r *fakeReadyRepo) AbandonReadyCheck(_ context.Context, _ string) error {
	r.abandoned = true
	return nil
}

type readyRequeueSpy struct{ agents []string }

func (q *readyRequeueSpy) Requeue(_ context.Context, agent, owner string, bid int64) error {
	q.agents = append(q.agents, agent)
	return nil
}

func readySeat(agent string, ready bool, asks int, askedAgo time.Duration, now time.Time) match.ReadySeat {
	s := match.ReadySeat{AgentPublicID: agent, OwnerPublicID: "u_" + agent, Asks: asks}
	if ready {
		t := now.Add(-time.Second)
		s.ReadyAt = &t
	}
	if asks > 0 {
		t := now.Add(-askedAgo)
		s.AskedAt = &t
	}
	return s
}

// ── the money rule ───────────────────────────────────────────────────────────

func TestEscrowHappensOnlyWhenEverySeatIsReady(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	cases := []struct {
		name       string
		seats      []match.ReadySeat
		wantStakes int
	}{
		{
			// The one case that may take money.
			name:       "both ready",
			seats:      []match.ReadySeat{readySeat("a", true, 1, time.Second, now), readySeat("b", true, 1, time.Second, now)},
			wantStakes: 1,
		},
		{
			// The failure this whole feature exists to prevent: today CreatePaired would
			// already have escrowed both stakes here.
			name:       "one silent, still inside its window",
			seats:      []match.ReadySeat{readySeat("a", true, 1, time.Second, now), readySeat("b", false, 1, time.Second, now)},
			wantStakes: 0,
		},
		{
			name:       "one out of asks — dropped",
			seats:      []match.ReadySeat{readySeat("a", true, 1, time.Second, now), readySeat("b", false, 2, time.Hour, now)},
			wantStakes: 0,
		},
		{
			name:       "neither has answered",
			seats:      []match.ReadySeat{readySeat("a", false, 0, 0, now), readySeat("b", false, 0, 0, now)},
			wantStakes: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, wallet, rr, _ := readySvc(t, now)
			rr.seats = tc.seats
			rr.activateOK = true
			repo.putReadyMatch(readyMatch("m_1"))

			if _, err := svc.ReadyTick(context.Background(), "m_1"); err != nil {
				t.Fatalf("tick: %v", err)
			}
			if got := wallet.stakes(); got != tc.wantStakes {
				t.Errorf("escrowed %d times, want %d — a seat that never agreed to play must not be staked",
					got, tc.wantStakes)
			}
		})
	}
}

func TestAFailedEscrowTakesNothingAndDoesNotStart(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	svc, repo, wallet, rr, _ := readySvc(t, now)
	wallet.failNext = true
	rr.seats = []match.ReadySeat{readySeat("a", true, 1, time.Second, now), readySeat("b", true, 1, time.Second, now)}
	rr.activateOK = true
	repo.putReadyMatch(readyMatch("m_1"))

	if _, err := svc.ReadyTick(context.Background(), "m_1"); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if rr.activated {
		t.Error("activated a match whose stakes could not be taken — it would pay out coins nobody staked")
	}
	if !rr.abandoned {
		t.Error("a table that cannot be escrowed must be released, not left holding its seats")
	}
}

func TestLosingTheActivationRaceRefunds(t *testing.T) {
	// Two sweepers, one table. The loser must not leave its stakes behind: money taken by an
	// attempt that did not start the match has to come back, or it is stranded with no match
	// to settle it.
	now := time.Unix(1_700_000_000, 0).UTC()
	svc, repo, wallet, rr, _ := readySvc(t, now)
	rr.seats = []match.ReadySeat{readySeat("a", true, 1, time.Second, now), readySeat("b", true, 1, time.Second, now)}
	rr.activateOK = false // another sweeper already started it
	repo.putReadyMatch(readyMatch("m_1"))

	if _, err := svc.ReadyTick(context.Background(), "m_1"); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if wallet.stakes() != 1 || wallet.refunds() != 1 {
		t.Errorf("staked %d refunded %d; the loser of the race must return what it took",
			wallet.stakes(), wallet.refunds())
	}
}

// ── who is asked, and who is let go ──────────────────────────────────────────

func TestASilentSeatIsAskedBeforeItIsDropped(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	svc, repo, _, rr, q := readySvc(t, now)
	// One ask, expired — it gets a second chance, not a removal.
	rr.seats = []match.ReadySeat{readySeat("a", true, 1, time.Second, now), readySeat("b", false, 1, time.Hour, now)}
	repo.putReadyMatch(readyMatch("m_1"))

	done, err := svc.ReadyTick(context.Background(), "m_1")
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if done {
		t.Error("a re-ask must not end the table")
	}
	if len(rr.asks) != 1 || rr.asks[0] != "b" {
		t.Errorf("asked %v, want a second ask for b", rr.asks)
	}
	if len(q.agents) != 0 {
		t.Errorf("requeued %v on a re-ask — nobody has been dropped yet", q.agents)
	}
}

func TestADroppedSeatIsRequeuedAndSoAreTheSeatsThatAnswered(t *testing.T) {
	// A seat that missed its window loses its place, not its money — and the agents that WERE
	// on time did nothing wrong, so they go back to the queue rather than sit behind a table
	// that can no longer fill.
	now := time.Unix(1_700_000_000, 0).UTC()
	svc, repo, wallet, rr, q := readySvc(t, now)
	rr.seats = []match.ReadySeat{readySeat("a", true, 1, time.Second, now), readySeat("b", false, 2, time.Hour, now)}
	repo.putReadyMatch(readyMatch("m_1"))

	if _, err := svc.ReadyTick(context.Background(), "m_1"); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if wallet.stakes() != 0 {
		t.Error("a dropped table must take nothing")
	}
	if !rr.abandoned {
		t.Error("a table below its roster cannot continue and must be released")
	}
	seen := map[string]bool{}
	for _, a := range q.agents {
		seen[a] = true
	}
	if !seen["b"] {
		t.Error("the dropped seat was not requeued — being briefly unreachable would mean falling out of the arena")
	}
	if !seen["a"] {
		t.Error("the seat that answered on time was not requeued")
	}
}

func TestReadyIsRefusedOnceTheTableHasStarted(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	svc, repo, _, _, _ := readySvc(t, now)
	m := readyMatch("m_1")
	m.Status = match.StatusActive
	repo.putReadyMatch(m)

	if err := svc.Ready(context.Background(), "a", "m_1"); err == nil {
		t.Error("acknowledging a match that already started should be refused — there is nothing left to wait for")
	}
}

func TestReadyIsRefusedForSomeoneNotSeated(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	svc, repo, _, _, _ := readySvc(t, now)
	repo.putReadyMatch(readyMatch("m_1"))

	if err := svc.Ready(context.Background(), "ag_stranger", "m_1"); err == nil {
		t.Error("a non-player must not be able to ready a table it is not seated at")
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// putReadyMatch seats a match at an arbitrary status. Named apart from the helper on the
// active-window branch so the two can coexist if both land.
func (r *fakeRepo) putReadyMatch(m match.Match) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.matches[m.PublicID] = m
}

// readySvc builds a service with the ready check installed and a wallet that records every
// coin movement, which is the only thing these tests actually assert on.
func readySvc(t *testing.T, now time.Time) (*match.Service, *fakeRepo, *readyWallet, *fakeReadyRepo, *readyRequeueSpy) {
	t.Helper()
	repo := newFakeRepo()
	wallet := &readyWallet{}
	rr := &fakeReadyRepo{}
	q := &readyRequeueSpy{}
	svc := match.New(repo, fakeLocker{}, match.NoopLimits{}, wallet,
		match.NoopBroadcaster{}, match.AllowAllVerifier{}, match.NoopRater{}, match.NoopFinishHook{},
		platform.FixedClock{T: now},
		match.Config{MoveWindow: 20 * time.Second, RakePct: 5, Rounds: 13, LockTTL: 5 * time.Second})
	svc.SetReadyCheck(rr, nil, q)
	return svc, repo, wallet, rr, q
}

func readyMatch(id string) match.Match {
	return match.Match{
		PublicID: id, Game: "goofspiel", Status: match.StatusReadyCheck, Bid: 100,
		Players: []match.Player{
			{AgentPublicID: "a", OwnerPublicID: "u_a", Seat: 0},
			{AgentPublicID: "b", OwnerPublicID: "u_b", Seat: 1},
		},
	}
}
