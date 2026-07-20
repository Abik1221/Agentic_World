package main

import (
	"context"
	"fmt"

	"github.com/agent-arena/arena/internal/autoplay"
	"github.com/agent-arena/arena/internal/mafia"
	"github.com/agent-arena/arena/internal/matchmaking"
	"github.com/agent-arena/arena/internal/monopoly"
	"github.com/agent-arena/arena/internal/sandbox"
)

// rankedQueueAdapter lets the auto-play reconciler use the real matchmaking queue.
// Enqueue rides the queue's existing certification + affordability + guardrail
// gates, so an unaffordable/capped/cooling-down agent is simply skipped.
type rankedQueueAdapter struct{ mm *matchmaking.Service }

func (a rankedQueueAdapter) Enqueue(ctx context.Context, agent, owner string, bid int64) error {
	_, err := a.mm.Enqueue(ctx, agent, owner, bid)
	return err
}

func (a rankedQueueAdapter) Queued(ctx context.Context, agent string) (bool, error) {
	e, err := a.mm.Status(ctx, agent)
	if err != nil {
		return false, nil // no live entry ⇒ not queued (top up on the next tick)
	}
	return e.Status == matchmaking.StatusWaiting || e.Status == matchmaking.StatusMatched, nil
}

// sandboxStarterAdapter starts a free practice match for an available agent and
// bounds concurrency via a time-window throttle (no per-driver finish callback
// needed). It routes to whichever game the setting rotates to.
type sandboxStarterAdapter struct {
	throttle *autoplay.SandboxThrottle
	goof     *sandbox.Service
	mafia    *mafia.Service
	monopoly *monopoly.Service
}

func (a sandboxStarterAdapter) StartSandbox(ctx context.Context, game, agent, owner string) error {
	var err error
	switch game {
	case "goofspiel":
		_, err = a.goof.StartPushPlay(ctx, agent, owner, "medium")
	case "mafia":
		_, err = a.mafia.StartPushPlay(ctx, agent, owner)
	case "monopoly":
		_, err = a.monopoly.StartPushPlay(ctx, agent, owner, 4)
	default:
		return fmt.Errorf("autoplay: unknown sandbox game %q", game)
	}
	if err == nil {
		a.throttle.Record(agent)
	}
	return err
}

func (a sandboxStarterAdapter) ActiveCount(_ context.Context, agent string) (int, error) {
	return a.throttle.ActiveCount(agent), nil
}
