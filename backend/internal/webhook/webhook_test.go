package webhook

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
)

// fakeStore is an in-memory Store. ClaimDue returns its queued batch once.
type fakeStore struct {
	mu          sync.Mutex
	due         []Delivery
	delivered   []string
	rescheduled map[string]time.Time
	deferred    []string
	gaveUp      map[string]string
}

func newFakeStore(due ...Delivery) *fakeStore {
	return &fakeStore{due: due, rescheduled: map[string]time.Time{}, gaveUp: map[string]string{}}
}

func (f *fakeStore) ClaimDue(_ context.Context, _, _ int, _ time.Duration) ([]Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.due
	f.due = nil
	return b, nil
}
func (f *fakeStore) MarkDelivered(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delivered = append(f.delivered, id)
	return nil
}
func (f *fakeStore) Reschedule(_ context.Context, id string, next time.Time, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rescheduled[id] = next
	return nil
}
func (f *fakeStore) Defer(_ context.Context, id string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deferred = append(f.deferred, id)
	return nil
}
func (f *fakeStore) GiveUp(_ context.Context, id, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gaveUp[id] = reason
	return nil
}

type fakeResolver struct {
	target agentclient.Target
	found  bool
}

func (r fakeResolver) PlayTarget(context.Context, string) (agentclient.Target, bool, error) {
	return r.target, r.found, nil
}

type fakeSender struct {
	mu      sync.Mutex
	err     error
	events  int
	gameEnd int
}

func (s *fakeSender) Event(context.Context, agentclient.Target, agentclient.EventNotification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events++
	return s.err
}
func (s *fakeSender) GameEnd(context.Context, agentclient.Target, agentclient.GameEndNotification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gameEnd++
	return s.err
}

func target() agentclient.Target {
	return agentclient.Target{EndpointURL: "https://a.example.com/turn", Token: "sek"}
}

func TestDispatcher_DeliversAndMarksDone(t *testing.T) {
	store := newFakeStore(
		Delivery{PublicID: "whk_1", AgentPublicID: "ag_1", Kind: KindEvent, Game: "goofspiel", MatchID: "m1", Seq: 1},
		Delivery{PublicID: "whk_2", AgentPublicID: "ag_1", Kind: KindGameEnd, Game: "goofspiel", MatchID: "m1"},
	)
	sender := &fakeSender{}
	d := NewDispatcher(store, fakeResolver{target(), true}, sender, NewHealthTracker(HealthConfig{}), nil, Config{})
	if err := d.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.delivered) != 2 {
		t.Fatalf("want 2 delivered, got %v", store.delivered)
	}
	if sender.events != 1 || sender.gameEnd != 1 {
		t.Fatalf("want 1 event + 1 game-end sent, got %d/%d", sender.events, sender.gameEnd)
	}
}

func TestDispatcher_RetriesOnFailure(t *testing.T) {
	store := newFakeStore(Delivery{PublicID: "whk_1", AgentPublicID: "ag_1", Kind: KindEvent, MatchID: "m1", Attempts: 0})
	sender := &fakeSender{err: errors.New("boom")}
	d := NewDispatcher(store, fakeResolver{target(), true}, sender, NewHealthTracker(HealthConfig{}), nil, Config{})
	if err := d.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.delivered) != 0 {
		t.Fatal("must not mark delivered on failure")
	}
	if _, ok := store.rescheduled["whk_1"]; !ok {
		t.Fatal("failed delivery must be rescheduled with backoff")
	}
}

func TestDispatcher_GivesUpAfterMaxAttempts(t *testing.T) {
	// Attempts already at MaxAttempts-1, so this attempt exhausts them.
	store := newFakeStore(Delivery{PublicID: "whk_1", AgentPublicID: "ag_1", Kind: KindEvent, MatchID: "m1", Attempts: 11})
	sender := &fakeSender{err: errors.New("boom")}
	d := NewDispatcher(store, fakeResolver{target(), true}, sender, NewHealthTracker(HealthConfig{}), nil, Config{MaxAttempts: 12})
	if err := d.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.gaveUp["whk_1"]; !ok {
		t.Fatal("must give up after exhausting attempts")
	}
	if _, ok := store.rescheduled["whk_1"]; ok {
		t.Fatal("must not reschedule once given up")
	}
}

func TestDispatcher_SkipsUnhealthyEndpoint(t *testing.T) {
	health := NewHealthTracker(HealthConfig{FailThreshold: 1, BaseCooldown: time.Minute})
	health.RecordFailure(target().EndpointURL, "down") // opens the circuit immediately
	store := newFakeStore(Delivery{PublicID: "whk_1", AgentPublicID: "ag_1", Kind: KindEvent, MatchID: "m1"})
	sender := &fakeSender{}
	d := NewDispatcher(store, fakeResolver{target(), true}, sender, health, nil, Config{})
	if err := d.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sender.events != 0 {
		t.Fatal("must not send to an endpoint with an open circuit")
	}
	if len(store.deferred) != 1 {
		t.Fatalf("open-circuit delivery must be deferred (not failed), got %v", store.deferred)
	}
}

func TestDispatcher_GivesUpWhenNoEndpoint(t *testing.T) {
	store := newFakeStore(Delivery{PublicID: "whk_1", AgentPublicID: "ag_gone", Kind: KindEvent, MatchID: "m1"})
	d := NewDispatcher(store, fakeResolver{found: false}, &fakeSender{}, NewHealthTracker(HealthConfig{}), nil, Config{})
	if err := d.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.gaveUp["whk_1"]; !ok {
		t.Fatal("must give up when the agent has no active endpoint")
	}
}

func TestHealthTracker_CircuitOpensAndCloses(t *testing.T) {
	h := NewHealthTracker(HealthConfig{FailThreshold: 3, BaseCooldown: time.Minute})
	url := "https://x.example.com/turn"
	if !h.Allow(url) {
		t.Fatal("unknown endpoint should be allowed")
	}
	h.RecordFailure(url, "e")
	h.RecordFailure(url, "e")
	if !h.Allow(url) {
		t.Fatal("circuit should stay closed below threshold")
	}
	h.RecordFailure(url, "e") // 3rd — opens
	if h.Allow(url) {
		t.Fatal("circuit should be open at threshold")
	}
	h.RecordSuccess(url, 5)
	if !h.Allow(url) {
		t.Fatal("a success should close the circuit")
	}
	snap := h.Snapshot()
	if len(snap) != 1 || !snap[0].Healthy || snap[0].LastLatencyMs != 5 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}

// The backlog's age bound. Without it the circuit-breaker path is unbounded: a defer
// does not count an attempt (correctly — nothing was tried), so an endpoint that stays
// down is deferred forever. Each defer rewrites an INDEXED column, so on the lab
// database that loop produced 79.5M non-HOT updates against 93,831 live rows, 242,591
// standing dead tuples, and a claim of 64 due deliveries that had to walk 883 MB of
// bloated index. These tests exist so that loop cannot come back.

func ageBoundDispatcher(st *fakeStore, maxAge time.Duration, now time.Time) *Dispatcher {
	d := NewDispatcher(st,
		fakeResolver{target: agentclient.Target{EndpointURL: "http://agent.invalid/play"}, found: true},
		&fakeSender{}, nil, nil, Config{MaxAge: maxAge})
	d.now = func() time.Time { return now }
	return d
}

func TestDeliveryOlderThanTheBacklogWindowIsAbandoned(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	st := newFakeStore()
	d := ageBoundDispatcher(st, 24*time.Hour, now)

	d.process(context.Background(), Delivery{
		PublicID: "whd_old", AgentPublicID: "agt_1", CreatedAt: now.Add(-25 * time.Hour),
	})

	if _, ok := st.gaveUp["whd_old"]; !ok {
		t.Fatalf("gaveUp = %v, want whd_old — a delivery past the window must leave the "+
			"backlog, or it is deferred forever and bloats every index on the table", st.gaveUp)
	}
	if len(st.delivered) != 0 {
		t.Fatalf("an expired delivery was still sent: %v", st.delivered)
	}
	if len(st.deferred) != 0 {
		t.Fatalf("an expired delivery was deferred again: %v — that is the loop this bound "+
			"exists to break", st.deferred)
	}
}

// The bound must not eat live traffic.
func TestDeliveryInsideTheWindowIsStillDelivered(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	st := newFakeStore()
	d := ageBoundDispatcher(st, 24*time.Hour, now)

	d.process(context.Background(), Delivery{
		PublicID: "whd_fresh", AgentPublicID: "agt_1", CreatedAt: now.Add(-1 * time.Hour),
	})

	if len(st.gaveUp) != 0 {
		t.Fatalf("abandoned a delivery inside the window: %v", st.gaveUp)
	}
	if len(st.delivered) != 1 || st.delivered[0] != "whd_fresh" {
		t.Fatalf("delivered = %v, want [whd_fresh]", st.delivered)
	}
}

// A row written before created_at was selected arrives zero-valued. It must be treated
// as ageless, not as infinitely old — otherwise the deploy that introduced the bound
// would bin the entire standing backlog on its first tick.
func TestZeroCreatedAtIsNotTreatedAsExpired(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	st := newFakeStore()
	d := ageBoundDispatcher(st, time.Nanosecond, now)

	d.process(context.Background(), Delivery{PublicID: "whd_zero", AgentPublicID: "agt_1"})

	if len(st.gaveUp) != 0 {
		t.Fatalf("abandoned a delivery with no CreatedAt: %v — an unknown age must not read "+
			"as an expired one", st.gaveUp)
	}
}

// The default matters: a zero MaxAge must not mean "expire everything instantly".
func TestMaxAgeDefaultsRatherThanExpiringEverything(t *testing.T) {
	if got := (Config{}).withDefaults().MaxAge; got != 24*time.Hour {
		t.Fatalf("default MaxAge = %v, want 24h — zero must not read as an instant expiry", got)
	}
}
