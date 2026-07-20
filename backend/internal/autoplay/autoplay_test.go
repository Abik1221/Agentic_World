package autoplay

import (
	"context"
	"errors"
	"testing"
)

type fakeRepo struct{ settings []Setting }

func (f *fakeRepo) ListEnabled(context.Context) ([]Setting, error) { return f.settings, nil }
func (f *fakeRepo) Get(context.Context, string) (Setting, bool, error) {
	return Setting{}, false, nil
}
func (f *fakeRepo) Set(context.Context, Setting) error { return nil }

type fakeQueue struct {
	queued    map[string]bool
	enqueued  []string
	enqErr    error
}

func (f *fakeQueue) Enqueue(_ context.Context, agent, _ string, _ int64) error {
	if f.enqErr != nil {
		return f.enqErr
	}
	f.enqueued = append(f.enqueued, agent)
	return nil
}
func (f *fakeQueue) Queued(_ context.Context, agent string) (bool, error) {
	return f.queued[agent], nil
}

type fakeSandbox struct {
	active  map[string]int
	started []string
}

func (f *fakeSandbox) StartSandbox(_ context.Context, game, agent, _ string) error {
	f.started = append(f.started, agent+":"+game)
	return nil
}
func (f *fakeSandbox) ActiveCount(_ context.Context, agent string) (int, error) {
	return f.active[agent], nil
}

func TestTick_RankedEnqueuesIdleAgentOnly(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "idle", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100},
		{AgentPublicID: "busy", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100},
	}}
	q := &fakeQueue{queued: map[string]bool{"busy": true}}
	New(repo, q, nil, Config{}, nil).Tick(context.Background())

	if len(q.enqueued) != 1 || q.enqueued[0] != "idle" {
		t.Fatalf("expected only the idle agent enqueued, got %v", q.enqueued)
	}
}

func TestTick_RankedSwallowsGuardrailRejections(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "broke", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100},
	}}
	// Enqueue rejects (e.g. insufficient funds / daily loss cap) — Tick must not panic.
	q := &fakeQueue{queued: map[string]bool{}, enqErr: errors.New("insufficient_funds")}
	New(repo, q, nil, Config{}, nil).Tick(context.Background())
	if len(q.enqueued) != 0 {
		t.Fatalf("rejected enqueue should record nothing, got %v", q.enqueued)
	}
}

func TestTick_SandboxStartsUpToConcurrencyCeiling(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "free", OwnerPublicID: "o", Enabled: true, Mode: ModeSandbox},
		{AgentPublicID: "atcap", OwnerPublicID: "o", Enabled: true, Mode: ModeSandbox},
	}}
	sb := &fakeSandbox{active: map[string]int{"atcap": 1}} // atcap already running one
	New(repo, nil, sb, Config{MaxSandboxConcurrent: 1}, nil).Tick(context.Background())

	if len(sb.started) != 1 || sb.started[0] != "free:"+DefaultGame {
		t.Fatalf("expected one sandbox start for the idle agent at default game, got %v", sb.started)
	}
}

func TestTick_SandboxRotatesGames(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "a", OwnerPublicID: "o", Enabled: true, Mode: ModeSandbox, Games: []string{"mafia", "monopoly"}},
	}}
	sb := &fakeSandbox{active: map[string]int{}}
	s := New(repo, nil, sb, Config{MaxSandboxConcurrent: 99}, nil)
	s.Tick(context.Background())
	s.Tick(context.Background())
	s.Tick(context.Background())
	want := []string{"a:mafia", "a:monopoly", "a:mafia"}
	if len(sb.started) != 3 || sb.started[0] != want[0] || sb.started[1] != want[1] || sb.started[2] != want[2] {
		t.Fatalf("games should round-robin %v, got %v", want, sb.started)
	}
}

func TestTick_IgnoresDisabledAndBlankMode(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "off", OwnerPublicID: "o", Enabled: false, Mode: ModeRanked, Bid: 100},
		{AgentPublicID: "nomode", OwnerPublicID: "o", Enabled: true, Bid: 100},
	}}
	q := &fakeQueue{queued: map[string]bool{}}
	sb := &fakeSandbox{active: map[string]int{}}
	New(repo, q, sb, Config{}, nil).Tick(context.Background())
	if len(q.enqueued) != 0 || len(sb.started) != 0 {
		t.Fatalf("disabled + unknown-mode agents must be untouched: q=%v sb=%v", q.enqueued, sb.started)
	}
}
