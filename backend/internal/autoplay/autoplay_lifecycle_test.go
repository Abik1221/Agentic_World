package autoplay

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestTick_KeepsPlayingAcrossGames models the continuous "deploy once, keep
// playing" loop the reconciler exists to provide: while an agent is in a match it
// is left alone, and the moment its game ends (it drops out of the queue) the very
// next tick re-enqueues it for another match. This is the answer to "when a game
// ends, how does the agent get added back to play?" — the reconciler, not the game
// service, does it.
func TestTick_KeepsPlayingAcrossGames(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "loop", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100},
	}}
	q := &fakeQueue{queued: map[string]bool{}}
	svc := New(repo, q, nil, Config{}, nil)
	ctx := context.Background()

	// Tick 1: idle -> enqueued for its first match.
	svc.Tick(ctx)
	if len(q.enqueued) != 1 {
		t.Fatalf("first tick should enqueue the idle agent once, got %v", q.enqueued)
	}

	// It gets matched and plays: while queued/in-match, further ticks are no-ops.
	q.queued["loop"] = true
	svc.Tick(ctx)
	svc.Tick(ctx)
	if len(q.enqueued) != 1 {
		t.Fatalf("no re-enqueue while the agent is still in a match, got %v", q.enqueued)
	}

	// The game ends -> the agent leaves the queue -> the next tick re-enqueues it.
	q.queued["loop"] = false
	svc.Tick(ctx)
	if len(q.enqueued) != 2 {
		t.Fatalf("a finished agent must be re-enqueued for the next match, got %v", q.enqueued)
	}
}

// TestTick_RankedOfflineSkippedThenResumes proves the money-leak fix at the
// reconciler layer. The reachability gate now lives in matchmaking.Enqueue (an
// offline agent is rejected with ErrAgentOffline before it can be matched); the
// reconciler's contract is to SWALLOW that rejection and retry, so:
//   - while the agent is offline, Enqueue keeps rejecting → nothing is queued, no
//     match is staked, no stake bleeds; and
//   - the moment the agent reconnects (Enqueue stops rejecting), the next tick
//     enqueues it automatically — no human re-trigger needed.
func TestTick_RankedOfflineSkippedThenResumes(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "flappy", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100},
	}}
	// Offline: the real matchmaking.Enqueue would return ErrAgentOffline here.
	q := &fakeQueue{queued: map[string]bool{}, enqErr: errors.New("agent_offline")}
	svc := New(repo, q, nil, Config{}, nil)
	ctx := context.Background()

	svc.Tick(ctx)
	svc.Tick(ctx)
	if len(q.enqueued) != 0 {
		t.Fatalf("an offline agent must NOT be enqueued (would forfeit-bleed its stake), got %v", q.enqueued)
	}

	// Agent reconnects → Enqueue stops rejecting → next tick resumes it.
	q.enqErr = nil
	svc.Tick(ctx)
	if len(q.enqueued) != 1 || q.enqueued[0] != "flappy" {
		t.Fatalf("a reconnected agent should resume automatically, got %v", q.enqueued)
	}
}

// The reconciler must RECORD why an agent isn't playing, so the developer can see it
// instead of auto-play silently going quiet. An offline agent (enqueue rejected) is
// recorded as "blocked" with the rejection reason.
func TestTick_RecordsBlockedReasonWhenOffline(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "offline", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100},
	}}
	q := &fakeQueue{queued: map[string]bool{}, enqErr: errors.New("agent not reachable")}
	New(repo, q, nil, Config{}, nil).Tick(context.Background())

	if len(repo.statuses) != 1 || repo.statuses[0] != "offline|"+StatusBlocked+"|agent not reachable" {
		t.Fatalf("expected a blocked status carrying the reason, got %v", repo.statuses)
	}
}

// A queued/in-match agent is recorded as "playing" — positive visibility, not just
// failures, so the developer can confirm it IS working.
func TestTick_RecordsPlayingWhenQueued(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "live", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100},
	}}
	q := &fakeQueue{queued: map[string]bool{"live": true}}
	New(repo, q, nil, Config{}, nil).Tick(context.Background())

	if len(repo.statuses) != 1 || !strings.HasPrefix(repo.statuses[0], "live|"+StatusPlaying+"|") {
		t.Fatalf("expected a playing status, got %v", repo.statuses)
	}
}

// A schedule/stop-condition pause is recorded as "paused" with the owner-facing
// reason (distinct from "blocked" — this is a choice the owner made, not a failure).
func TestTick_RecordsPausedReasonForStopCondition(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "capped", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100, DailyLossStop: 300},
	}}
	q := &fakeQueue{queued: map[string]bool{}}
	svc := New(repo, q, nil, Config{}, nil)
	svc.SetStats(fakeStats{"capped": {LossCoins: 300}})
	svc.Tick(context.Background())

	if len(repo.statuses) != 1 || repo.statuses[0] != "capped|"+StatusPaused+"|daily loss-stop reached" {
		t.Fatalf("expected a paused status with the stop reason, got %v", repo.statuses)
	}
	if len(q.enqueued) != 0 {
		t.Fatalf("a paused agent must not be enqueued, got %v", q.enqueued)
	}
}

// Status is written only on a CHANGE — a steadily-playing agent (whose stored status
// already matches) incurs no write churn each tick.
func TestTick_WritesStatusOnlyOnChange(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "steady", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100,
			LastStatus: StatusPlaying, LastStatusReason: "in a ranked match or waiting in the queue"},
	}}
	q := &fakeQueue{queued: map[string]bool{"steady": true}} // still playing → same status
	New(repo, q, nil, Config{}, nil).Tick(context.Background())

	if len(repo.statuses) != 0 {
		t.Fatalf("unchanged status must not be re-written, got %v", repo.statuses)
	}
}

// A ranked agent whose game is an N-player game (Mafia/Monopoly) is routed to the
// GROUP queue, not the 2-player one — hands-free ranked play for those games.
func TestTick_RankedRoutesGroupGameToGroupQueue(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "maf", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100, Games: []string{"mafia"}},
	}}
	two := &fakeQueue{queued: map[string]bool{}}
	group := &fakeGroupQueue{games: map[string]bool{"mafia": true, "monopoly": true}, queued: map[string]bool{}}
	svc := New(repo, two, nil, Config{}, nil)
	svc.SetGroupQueue(group)
	svc.Tick(context.Background())

	if len(group.enqueued) != 1 || group.enqueued[0] != "maf:mafia" {
		t.Fatalf("mafia ranked should enqueue into the group queue, got %v", group.enqueued)
	}
	if len(two.enqueued) != 0 {
		t.Fatalf("a group game must NOT hit the 2-player queue, got %v", two.enqueued)
	}
}

// A ranked agent whose game is Goofspiel (or unset → default) stays on the 2-player
// queue even when a group queue is wired.
func TestTick_RankedRoutesGoofspielToTwoPlayerQueue(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "goo", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100, Games: []string{"goofspiel"}},
		{AgentPublicID: "def", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100}, // no games → default goofspiel
	}}
	two := &fakeQueue{queued: map[string]bool{}}
	group := &fakeGroupQueue{games: map[string]bool{"mafia": true, "monopoly": true}, queued: map[string]bool{}}
	svc := New(repo, two, nil, Config{}, nil)
	svc.SetGroupQueue(group)
	svc.Tick(context.Background())

	if len(group.enqueued) != 0 {
		t.Fatalf("goofspiel/default must NOT hit the group queue, got %v", group.enqueued)
	}
	if len(two.enqueued) != 2 {
		t.Fatalf("goofspiel + default should both hit the 2-player queue, got %v", two.enqueued)
	}
}

// A group-game agent already in a table/queue is left alone (no double-enqueue).
func TestTick_RankedGroupSkipsWhenAlreadyQueued(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "busy", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100, Games: []string{"monopoly"}},
	}}
	group := &fakeGroupQueue{games: map[string]bool{"monopoly": true}, queued: map[string]bool{"busy": true}}
	svc := New(repo, &fakeQueue{queued: map[string]bool{}}, nil, Config{}, nil)
	svc.SetGroupQueue(group)
	svc.Tick(context.Background())

	if len(group.enqueued) != 0 {
		t.Fatalf("an agent already in the group queue must not be re-enqueued, got %v", group.enqueued)
	}
	if len(repo.statuses) != 1 || !strings.HasPrefix(repo.statuses[0], "busy|"+StatusPlaying+"|") {
		t.Fatalf("expected a playing status for the queued group agent, got %v", repo.statuses)
	}
}

// TestTick_LossStopHaltsBleedingAgent is the companion: the DailyLossStop backstop
// DOES stop an agent that is losing (e.g. the offline agent forfeiting stakes) —
// but only because the owner explicitly configured it. With the default (0) this
// gate is off, which is why the gap above is a real money risk.
func TestTick_LossStopHaltsBleedingAgent(t *testing.T) {
	repo := &fakeRepo{settings: []Setting{
		{AgentPublicID: "bleeding", OwnerPublicID: "o", Enabled: true, Mode: ModeRanked, Bid: 100, DailyLossStop: 300},
	}}
	q := &fakeQueue{queued: map[string]bool{}}
	svc := New(repo, q, nil, Config{}, nil)
	svc.SetStats(fakeStats{"bleeding": {LossCoins: 300}}) // already lost the stop amount today
	svc.Tick(context.Background())

	if len(q.enqueued) != 0 {
		t.Fatalf("an agent past its daily loss-stop must not be re-enqueued, got %v", q.enqueued)
	}
}
