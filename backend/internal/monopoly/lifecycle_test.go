package monopoly

import (
	"context"
	"errors"
	"testing"
	"time"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
)

type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

type fakeBcast struct{}

func (fakeBcast) Broadcast(string, []mono.Event) {}
func (fakeBcast) BroadcastPending(string, []int) {}

type fakeLock struct{}

func (fakeLock) Lock(context.Context, string, time.Duration) (func(), bool, error) {
	return func() {}, true, nil
}

// fakeRepo implements monopoly.Repo; only the fields a test needs are set.
type fakeRepo struct {
	expireCutoff time.Time
	expireLimit  int
	expireN      int

	// Sweep-resilience knobs (see the mafia test for the shape).
	activeTemplate *Match
	expiredIDs     []string
	failWriteFor   string
	errBoom        error
	advanced       []string
}

func (f *fakeRepo) Create(context.Context, CreateMatchInput) (Match, error) { return Match{}, nil }
func (f *fakeRepo) CreateWaiting(context.Context, CreateMatchInput) (Match, error) {
	return Match{}, nil
}
func (f *fakeRepo) ListWaiting(context.Context, int64, string, int) ([]LobbyItem, error) {
	return nil, nil
}
func (f *fakeRepo) JoinSeat(context.Context, string, Player) error { return nil }
func (f *fakeRepo) Start(context.Context, string, mono.State, time.Time, []mono.Event) error {
	return nil
}
func (f *fakeRepo) CancelWaiting(context.Context, string, string) error { return nil }
func (f *fakeRepo) ExpireStaleWaiting(_ context.Context, cutoff time.Time, limit int) (int, error) {
	f.expireCutoff, f.expireLimit = cutoff, limit
	return f.expireN, nil
}
func (f *fakeRepo) Get(_ context.Context, id string) (Match, error) {
	if f.activeTemplate != nil {
		m := *f.activeTemplate
		m.PublicID = id
		return m, nil
	}
	return Match{}, nil
}
func (f *fakeRepo) Advance(_ context.Context, id string, _ mono.State, _ *time.Time, _ []mono.Event) error {
	if id == f.failWriteFor {
		return f.errBoom
	}
	f.advanced = append(f.advanced, id)
	return nil
}
func (f *fakeRepo) Finish(_ context.Context, id string, _ mono.State, _ int, _ string, _ []Player, _ []mono.Event) error {
	if id == f.failWriteFor {
		return f.errBoom
	}
	return nil
}
func (f *fakeRepo) ListActiveExpired(context.Context, string, time.Time, int) ([]string, error) {
	return f.expiredIDs, nil
}
func (f *fakeRepo) LoadEvents(context.Context, string, int) ([]mono.Event, error) { return nil, nil }
func (f *fakeRepo) LoadEventsTimed(context.Context, string) ([]TimedEvent, error) {
	return nil, nil
}
func (f *fakeRepo) LiveMatches(context.Context) ([]LiveMatch, error)        { return nil, nil }
func (f *fakeRepo) AgentSigningKey(context.Context, string) (string, error) { return "", nil }
func (f *fakeRepo) RecordMoveSignature(context.Context, string, int, int, string, string, string) error {
	return nil
}
func (f *fakeRepo) LoadMoveSignatures(context.Context, string) ([]MoveSig, error) { return nil, nil }

// SweepExpired must be best-effort per table: one wedged table (a write that errors)
// must NOT block the timeout — and escrow release — of the other expired tables.
// Verifies the batch-continue fix (previously it aborted on the first error).
func TestSweepExpired_OneWedgedTableDoesNotBlockTheRest(t *testing.T) {
	seed := make([]byte, 32)
	state, _ := mono.New(matchConfig(2)).Init(seed) // valid active state → ForceTimeout produces events
	past := time.Unix(1_000, 0)
	// One real agent at seat 0 keeps drive from finishing the game in a single tick,
	// so persist takes the Advance path we inject on.
	tmpl := &Match{Status: StatusActive, EntryFee: 0, Players: 2, Seed: seed, State: state,
		RoundDeadline: &past, Agents: []Player{{Seat: 0}}}

	repo := &fakeRepo{
		activeTemplate: tmpl,
		expiredIDs:     []string{"good-1", "bad", "good-2"},
		failWriteFor:   "bad",
		errBoom:        errors.New("db write failed"),
	}
	svc := NewService(repo, fakeLock{}, nil, fakeBcast{}, nil, fakeClock{t: time.Unix(2_000, 0)}, Config{})

	swept, err := svc.SweepExpired(context.Background(), 10)
	if err == nil {
		t.Fatal("expected the wedged table's error to be surfaced (joined), got nil")
	}
	if swept != 2 {
		t.Fatalf("the 2 healthy tables must still be swept despite the 1 wedged one, got %d", swept)
	}
	for _, id := range repo.advanced {
		if id == "bad" {
			t.Fatal("the wedged table must not have advanced")
		}
	}
}

// SweepStaleWaiting aborts staked waiting tables created before (now − WaitingTTL),
// so an agent isn't stranded in a lobby that never reaches TargetPlayers.
func TestSweepStaleWaiting_ComputesCutoffFromTTL(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{expireN: 2}
	svc := NewService(repo, nil, nil, fakeBcast{}, nil, fakeClock{t: now}, Config{WaitingTTL: 15 * time.Minute})

	n, err := svc.SweepStaleWaiting(context.Background(), 64)
	if err != nil {
		t.Fatalf("SweepStaleWaiting: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 tables aborted, got %d", n)
	}
	if want := now.Add(-15 * time.Minute); !repo.expireCutoff.Equal(want) {
		t.Fatalf("cutoff = %v, want now-TTL = %v", repo.expireCutoff, want)
	}
	if repo.expireLimit != 64 {
		t.Fatalf("limit = %d, want 64", repo.expireLimit)
	}
}
