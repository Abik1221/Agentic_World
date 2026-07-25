package autoplay

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRepo struct {
	settings []Setting
	statuses []string // "agent|status|reason" per SetStatus call (transition log)
}

func (f *fakeRepo) ListEnabled(context.Context) ([]Setting, error) { return f.settings, nil }
func (f *fakeRepo) Get(context.Context, string) (Setting, bool, error) {
	return Setting{}, false, nil
}
func (f *fakeRepo) Set(context.Context, Setting) error { return nil }
func (f *fakeRepo) SetStatus(_ context.Context, agent, status, reason string) error {
	f.statuses = append(f.statuses, agent+"|"+status+"|"+reason)
	return nil
}

type fakeQueue struct {
	queued   map[string]bool
	enqueued []string
	enqErr   error
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

// fakeGroupQueue is the N-player ranked queue; handles only the games in `games`.
type fakeGroupQueue struct {
	games    map[string]bool
	queued   map[string]bool
	enqueued []string // "agent:game" per Enqueue
	enqErr   error
}

func (f *fakeGroupQueue) Handles(game string) bool { return f.games[game] }
func (f *fakeGroupQueue) Enqueue(_ context.Context, agent, _, game string, _ int64) error {
	if f.enqErr != nil {
		return f.enqErr
	}
	f.enqueued = append(f.enqueued, agent+":"+game)
	return nil
}
func (f *fakeGroupQueue) Queued(_ context.Context, agent string) (bool, error) {
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

type fakeStats map[string]DailyStats

func (f fakeStats) Today(_ context.Context, agent string) (DailyStats, error) { return f[agent], nil }

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

func TestShouldPlay_StopConditions(t *testing.T) {
	base := Setting{Enabled: true, Mode: ModeRanked}
	cases := []struct {
		name string
		s    Setting
		st   DailyStats
		hour int
		want bool
	}{
		{"clean", base, DailyStats{}, 12, true},
		{"match cap hit", Setting{DailyMatchCap: 10}, DailyStats{Matches: 10}, 12, false},
		{"match cap under", Setting{DailyMatchCap: 10}, DailyStats{Matches: 9}, 12, true},
		{"token budget spent", Setting{DailyTokenBudget: 100000}, DailyStats{Tokens: 100000}, 12, false},
		{"token budget under", Setting{DailyTokenBudget: 100000}, DailyStats{Tokens: 50000}, 12, true},
		{"take-profit hit", Setting{TakeProfitCoins: 500}, DailyStats{NetCoins: 500}, 12, false},
		{"take-profit under", Setting{TakeProfitCoins: 500}, DailyStats{NetCoins: 400}, 12, true},
		{"loss-stop hit", Setting{DailyLossStop: 300}, DailyStats{LossCoins: 300}, 12, false},
		{"loss-stop under", Setting{DailyLossStop: 300}, DailyStats{LossCoins: 200}, 12, true},
	}
	for _, tc := range cases {
		if ok, _ := shouldPlay(tc.s, tc.st, tc.hour); ok != tc.want {
			t.Errorf("%s: shouldPlay=%v want %v", tc.name, ok, tc.want)
		}
	}
}

func TestWithinActiveHours(t *testing.T) {
	always := Setting{ActiveFromUTC: 0, ActiveUntilUTC: 0}
	day := Setting{ActiveFromUTC: 9, ActiveUntilUTC: 17}   // 9am–5pm
	night := Setting{ActiveFromUTC: 22, ActiveUntilUTC: 6} // wraps midnight
	checks := []struct {
		s    Setting
		hour int
		want bool
	}{
		{always, 3, true}, {always, 15, true},
		{day, 8, false}, {day, 9, true}, {day, 16, true}, {day, 17, false},
		{night, 23, true}, {night, 2, true}, {night, 6, false}, {night, 12, false},
	}
	for _, c := range checks {
		if got := withinActiveHours(c.s, c.hour); got != c.want {
			t.Errorf("window %+v hour %d = %v want %v", c.s, c.hour, got, c.want)
		}
	}
}

func TestTick_ScheduleAndStopGateBothModes(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "capped", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100, DailyMatchCap: 5},
		{AgentPublicID: "asleep", OwnerPublicID: "o", Enabled: true, Mode: ModeSandbox, ActiveFromUTC: 9, ActiveUntilUTC: 17},
	}}
	q := &fakeQueue{queued: map[string]bool{}}
	sb := &fakeSandbox{active: map[string]int{}}
	svc := New(repo, q, sb, Config{MaxSandboxConcurrent: 9}, nil)
	svc.now = func() time.Time { return time.Date(2026, 7, 20, 3, 0, 0, 0, time.UTC) } // 03:00 UTC
	svc.SetStats(fakeStats{"capped": {Matches: 5}})                                    // capped hit its cap
	svc.Tick(context.Background())
	if len(q.enqueued) != 0 {
		t.Fatalf("capped agent must not enqueue, got %v", q.enqueued)
	}
	if len(sb.started) != 0 {
		t.Fatalf("asleep agent (outside 9-17 at 03:00) must not start, got %v", sb.started)
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
