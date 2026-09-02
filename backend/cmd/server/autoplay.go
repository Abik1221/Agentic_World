package main

import (
	"context"
	"fmt"
	"time"

	"github.com/agent-arena/arena/internal/autoplay"
	"github.com/agent-arena/arena/internal/mafia"
	"github.com/agent-arena/arena/internal/matchmaking"
	"github.com/agent-arena/arena/internal/sandbox"
	"github.com/agent-arena/arena/internal/store"
	"github.com/agent-arena/arena/internal/wallet"
)

// autoplayStats feeds the auto-play stop-conditions today's per-agent activity:
// match count + token spend from the benchmark aggregate, and coins lost from the
// wallet (same day boundary as the daily_loss_limit guardrail). NetCoins is left
// 0 for now (take-profit needs a per-day gains figure the ledger doesn't expose
// yet); the daily loss-stop rides LossCoins, and the wallet guardrail is the hard
// backstop regardless.
type autoplayStats struct {
	pindex *store.PIndexRepo
	wallet *wallet.Service
	now    func() time.Time
}

func (a autoplayStats) Today(ctx context.Context, agentPublicID string) (autoplay.DailyStats, error) {
	n := a.now()
	dayStart := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, n.Location())
	matches, tokens, err := a.pindex.TodayStats(ctx, agentPublicID, dayStart)
	if err != nil {
		return autoplay.DailyStats{}, err
	}
	loss, err := a.wallet.LossToday(ctx, agentPublicID)
	if err != nil {
		return autoplay.DailyStats{}, err
	}
	net, err := a.wallet.NetToday(ctx, agentPublicID)
	if err != nil {
		return autoplay.DailyStats{}, err
	}
	return autoplay.DailyStats{Matches: matches, Tokens: tokens, LossCoins: loss, NetCoins: net}, nil
}

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
}

func (a sandboxStarterAdapter) StartSandbox(ctx context.Context, game, agent, owner string) error {
	var err error
	switch game {
	case "goofspiel":
		_, err = a.goof.StartPushPlay(ctx, agent, owner, "medium")
	case "mafia":
		_, err = a.mafia.StartPushPlay(ctx, agent, owner)
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

// ClearQueue removes agents' ranked-queue entries. Satisfied for match.QueueClearer.
//
// Deleting rather than resetting to 'waiting' is deliberate: an agent that finished a match has
// not asked for another one. Autoplay re-enters on its next tick if the owner enabled it, and an
// agent without autoplay stays out — which is the consent property the whole queue rests on.
func (a rankedQueueAdapter) ClearQueue(ctx context.Context, agentPublicIDs ...string) error {
	for _, id := range agentPublicIDs {
		if id == "" {
			continue
		}
		if err := a.mm.Cancel(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
