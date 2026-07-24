package mafia

import (
	"context"
	"errors"
	"testing"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
)

// ── shared fakes ──────────────────────────────────────────────────────────────

type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

// fakeLock returns lockErr from Lock (to simulate Redis being down) or grants the
// lock when lockErr is nil.
type fakeLock struct{ lockErr error }

func (l fakeLock) Lock(context.Context, string, time.Duration) (func(), bool, error) {
	if l.lockErr != nil {
		return nil, false, l.lockErr
	}
	return func() {}, true, nil
}

type fakeBcast struct{}

func (fakeBcast) Broadcast(string, []mf.Event) {}

// fakeRepo implements mafia.Repo; only the fields a given test needs are set.
type fakeRepo struct {
	getMatch     Match
	getErr       error
	expireCutoff time.Time
	expireLimit  int
	expireN      int

	// Sweep-resilience knobs: Get returns activeTemplate (with PublicID set to the
	// requested id) so each expired id resolves to a real active match; a write
	// (Advance/Finish) for failWriteFor returns errBoom, so exactly one table in the
	// batch "wedges".
	activeTemplate *Match
	expiredIDs     []string
	failWriteFor   string
	errBoom        error
	advanced       []string // ids that reached a successful Advance
}

func (f *fakeRepo) CreateWaiting(context.Context, CreateMatchInput) (Match, error) {
	return Match{}, nil
}
func (f *fakeRepo) ListWaiting(context.Context, int64, string, int) ([]LobbyItem, error) {
	return nil, nil
}
func (f *fakeRepo) Get(_ context.Context, id string) (Match, error) {
	if f.activeTemplate != nil {
		m := *f.activeTemplate
		m.PublicID = id
		return m, nil
	}
	return f.getMatch, f.getErr
}
func (f *fakeRepo) JoinSeat(context.Context, string, Player) error { return nil }
func (f *fakeRepo) Start(context.Context, string, map[int]string, mf.State, time.Time, []mf.Event) error {
	return nil
}
func (f *fakeRepo) Advance(_ context.Context, id string, _ mf.State, _ *time.Time, _ map[int]bool, _ []mf.Event) error {
	if id == f.failWriteFor {
		return f.errBoom
	}
	f.advanced = append(f.advanced, id)
	return nil
}
func (f *fakeRepo) Finish(_ context.Context, id string, _ mf.State, _, _ string, _ []Player, _ []mf.Event) error {
	if id == f.failWriteFor {
		return f.errBoom
	}
	return nil
}
func (f *fakeRepo) ListActiveExpired(context.Context, string, time.Time, int) ([]string, error) {
	return f.expiredIDs, nil
}
func (f *fakeRepo) LoadEvents(context.Context, string, int) ([]mf.Event, error) { return nil, nil }
func (f *fakeRepo) LiveMatches(context.Context) ([]LiveMatch, error)            { return nil, nil }
func (f *fakeRepo) CancelWaiting(context.Context, string, string) error         { return nil }
func (f *fakeRepo) ExpireStaleWaiting(_ context.Context, cutoff time.Time, limit int) (int, error) {
	f.expireCutoff, f.expireLimit = cutoff, limit
	return f.expireN, nil
}
func (f *fakeRepo) AgentSigningKey(context.Context, string) (string, error) { return "", nil }
func (f *fakeRepo) RecordMoveSignature(context.Context, string, int, int, string, string, string) error {
	return nil
}
func (f *fakeRepo) LoadMoveSignatures(context.Context, string) ([]MoveSig, error) { return nil, nil }

func newTestSvc(repo Repo, lock Locker, clk fakeClock, cfg Config) *Service {
	return NewService(repo, lock, nil, nil, fakeBcast{}, nil, nil, clk, cfg)
}

// ── Fix A: stale-waiting sweep ──────────────────────────────────────────────

// SweepStaleWaiting asks the repo to abort waiting tables created before (now −
// WaitingTTL), so a lobby that never fills its roster doesn't strand its agents.
func TestSweepStaleWaiting_ComputesCutoffFromTTL(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{expireN: 3}
	svc := newTestSvc(repo, fakeLock{}, fakeClock{t: now}, Config{WaitingTTL: 10 * time.Minute})

	n, err := svc.SweepStaleWaiting(context.Background(), 64)
	if err != nil {
		t.Fatalf("SweepStaleWaiting: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 tables aborted, got %d", n)
	}
	want := now.Add(-10 * time.Minute)
	if !repo.expireCutoff.Equal(want) {
		t.Fatalf("cutoff = %v, want now-TTL = %v", repo.expireCutoff, want)
	}
	if repo.expireLimit != 64 {
		t.Fatalf("limit = %d, want 64", repo.expireLimit)
	}
}

// SweepExpired must be best-effort per table: one wedged table (a write that errors)
// must NOT block the timeout — and therefore the escrow release — of every OTHER
// expired table in the batch. Verifies the batch-continue fix (previously the loop
// returned on the first error, aborting the whole sweep and reporting 0 swept).
func TestSweepExpired_OneWedgedTableDoesNotBlockTheRest(t *testing.T) {
	seed := make([]byte, 32)
	seats := make([]int, mf.RosterSize)
	for i := range seats {
		seats[i] = i + 1
	}
	state, _ := mf.New().Init(seed, seats) // a valid active night state → ForceTimeout produces events
	past := time.Unix(1_000, 0)
	tmpl := &Match{Status: StatusActive, EntryFee: 0, Seed: seed, State: state, RoundDeadline: &past}

	repo := &fakeRepo{
		activeTemplate: tmpl,
		expiredIDs:     []string{"good-1", "bad", "good-2"},
		failWriteFor:   "bad",
		errBoom:        errors.New("db write failed"),
	}
	// clock AFTER the deadline so every table is genuinely expired.
	svc := newTestSvc(repo, fakeLock{}, fakeClock{t: time.Unix(2_000, 0)}, Config{})

	swept, err := svc.SweepExpired(context.Background(), 10)
	if err == nil {
		t.Fatal("expected the wedged table's error to be surfaced (joined), got nil")
	}
	if swept != 2 {
		t.Fatalf("the 2 healthy tables must still be swept despite the 1 wedged one, got %d", swept)
	}
	// Both healthy tables actually advanced; the bad one did not.
	if len(repo.advanced) != 2 {
		t.Fatalf("expected 2 tables to advance, got %v", repo.advanced)
	}
	for _, id := range repo.advanced {
		if id == "bad" {
			t.Fatal("the wedged table must not have advanced")
		}
	}
}

// ── Fix B: request-path resilience parity ───────────────────────────────────

// When Redis is down (Lock errors), Act must NOT hard-fail on the lock error — it
// proceeds lockless and relies on optimistic concurrency, exactly like Goofspiel
// and Monopoly. Proven by Act reaching tryAct → repo.Get and surfacing the domain
// error (ErrNotActive here) rather than the raw lock error.
func TestAct_ProceedsLocklessWhenRedisDown(t *testing.T) {
	repo := &fakeRepo{getMatch: Match{Status: StatusWaiting}} // not active → tryAct returns ErrNotActive
	svc := newTestSvc(repo, fakeLock{lockErr: errors.New("redis unreachable")}, fakeClock{t: time.Unix(0, 0)}, Config{})

	_, err := svc.Act(context.Background(), "ag", "m1", mf.Action{}, 0, "", "", false)
	if !errors.Is(err, ErrNotActive) {
		t.Fatalf("with Redis down, Act should proceed lockless and reach tryAct (ErrNotActive), got %v", err)
	}
}

// Sanity: when the lock is held by another instance (granted=false, no error), Act
// still returns ErrBusy — the fast-path contention signal is preserved.
func TestAct_BusyWhenLockHeld(t *testing.T) {
	// fakeLock with nil err always grants; model "held" by returning ok=false via a
	// dedicated lock.
	held := heldLock{}
	repo := &fakeRepo{getMatch: Match{Status: StatusActive}}
	svc := newTestSvc(repo, held, fakeClock{t: time.Unix(0, 0)}, Config{})

	_, err := svc.Act(context.Background(), "ag", "m1", mf.Action{}, 0, "", "", false)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("a held lock should return ErrBusy, got %v", err)
	}
}

type heldLock struct{}

func (heldLock) Lock(context.Context, string, time.Duration) (func(), bool, error) {
	return nil, false, nil // no error, but not granted (another instance holds it)
}
