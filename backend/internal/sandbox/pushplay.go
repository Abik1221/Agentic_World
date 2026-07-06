package sandbox

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/remoteplay"
	"github.com/agent-arena/arena/internal/webhook"
)

// Driver is the slice of match.Service the push-play loop needs beyond opening a
// match: read the current per-seat state and submit a card for that seat. It is
// an interface so the push loop is unit-testable with a fake match.
type Driver interface {
	State(ctx context.Context, matchPublicID, viewerAgentPublicID string, wait bool, timeout time.Duration) (match.AgentView, error)
	Act(ctx context.Context, agentPublicID, matchPublicID string, round, card int, signature string) (match.AgentView, error)
}

// RemoteResolver returns the push-play Target (endpoint URL + bearer token) for an
// agent's active, endpoint-verified manifest. Implemented by manifest.Service.
type RemoteResolver interface {
	PlayTarget(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error)
}

// pushPlayer drives the developer's SEAT of a sandbox match by calling their
// hosted agent endpoint (the manifest "push" model) for each decision, while the
// house bot plays the other seat inline through the standard match machinery. It
// runs entirely server-side; the browser watches the result live over the normal
// spectator SSE stream. Nil until EnablePushPlay is called.
type pushPlayer struct {
	driver Driver
	remote RemoteResolver
	client *agentclient.Client
	// enqueue is the durable webhook queue for async /event + /game-end. When set
	// (production), notifications are persisted and delivered by the central
	// dispatcher (retry + health-gated). When nil, they fall back to best-effort
	// inline goroutines so tests/standalone use still work.
	enqueue webhook.Enqueuer
	log     *slog.Logger
	// maxMatch caps a single driven match's wall-clock so a stalled/hostile
	// endpoint can never leak a goroutine forever.
	maxMatch time.Duration
}

// SetWebhookEnqueuer routes async /event + /game-end through the durable webhook
// queue. Call after EnablePushPlay; a no-op if push-play isn't enabled.
func (s *Service) SetWebhookEnqueuer(e webhook.Enqueuer) {
	if s.pusher != nil {
		s.pusher.enqueue = e
	}
}

// EnablePushPlay wires the optional /v1/sandbox/pushplay capability. Without it,
// StartPushPlay returns 501 and the feature is simply absent.
func (s *Service) EnablePushPlay(driver Driver, remote RemoteResolver, client *agentclient.Client, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	s.pusher = &pushPlayer{driver: driver, remote: remote, client: client, log: log, maxMatch: 3 * time.Minute}
}

// StartPushPlay opens a no-stakes sandbox match and drives the developer's seat
// from their hosted agent endpoint. The developer must have an active,
// endpoint-verified manifest (that is where the URL + bearer token come from);
// otherwise it returns a 400 so the UI can point them at registration. The match
// then plays itself to completion in the background and is watchable at
// /v1/match/{id}/watch, exactly like any other match.
func (s *Service) StartPushPlay(ctx context.Context, humanAgent, humanOwner, difficulty string) (StartResult, error) {
	if !s.enabled {
		return StartResult{}, httpx.NewError(403, "sandbox_disabled", "Sandbox practice mode is disabled in this environment.")
	}
	if s.pusher == nil {
		return StartResult{}, httpx.NewError(501, "pushplay_unavailable", "Push-play is not configured on this server.")
	}
	target, found, err := s.pusher.remote.PlayTarget(ctx, humanAgent)
	if err != nil {
		return StartResult{}, err
	}
	if !found || target.EndpointURL == "" {
		return StartResult{}, httpx.NewError(400, "no_verified_endpoint",
			"Register and verify your agent's endpoint (manifest) before running push-play.")
	}

	opp := opponentFor(difficulty)
	id, err := s.matches.CreateSandbox(ctx, humanAgent, humanOwner, opp.ID, HouseOwner, opp.Style)
	if err != nil {
		return StartResult{}, err
	}

	// Drive the developer's seat in the background: the request returns immediately
	// with a match id the client watches live. A fresh context (not the request's)
	// keeps the loop alive after the HTTP response, bounded by maxMatch.
	go s.pusher.drive(id, humanAgent, target)

	res := StartResult{MatchID: id, Mode: match.ModeSandbox, Opponent: opp}
	res.Driver = "remote"
	return res, nil
}

// drive polls the match for the developer's turn and, when it is their move, asks
// their endpoint for a card and submits it. A remote error/timeout/illegal move
// falls back to the lowest legal card so a bad endpoint can never wedge the match
// — the same guarantee remoteplay.PlayGoofspiel gives.
func (p *pushPlayer) drive(matchID, agentID string, target agentclient.Target) {
	ctx, cancel := context.WithTimeout(context.Background(), p.maxMatch)
	defer cancel()

	// Lifecycle: /initialize once at match start (best-effort — the agent may also
	// initialise lazily on the first /turn). Seat A is always the developer.
	if _, err := p.client.Initialize(ctx, target, agentclient.InitializeRequest{
		MatchID: matchID, Game: "goofspiel", Seat: 0, Players: 2,
	}); err != nil {
		p.log.Warn("pushplay: initialize failed (continuing)", "match", matchID, "err", err)
	}

	fallbacks := 0
	deliveredRounds := 0 // highest round already pushed to /event (async, ordered by seq)
	for {
		if ctx.Err() != nil {
			p.log.Warn("pushplay: driver deadline exceeded, stopping", "match", matchID)
			return
		}
		// Immediate read (wait=false): in a sandbox the house plays inline inside
		// the developer's Act, so it is almost always the developer's turn while the
		// match is active. A long-poll would stall here waiting for a change that
		// only this loop can cause.
		v, err := p.driver.State(ctx, matchID, agentID, false, 0)
		if err != nil {
			p.log.Warn("pushplay: state read failed, stopping driver", "match", matchID, "err", err)
			return
		}
		// Async /event notifications for rounds that resolved since the last read
		// (opponent card + winner). Fire-and-forget so the turn loop / engine never
		// blocks on delivery; the agent orders by Seq. (Production scale: a central
		// outbox dispatcher — see ONAVION_DEV_PLATFORM.md P2.)
		deliveredRounds = p.dispatchRoundEvents(ctx, agentID, target, matchID, v, deliveredRounds)
		if v.Status != "active" {
			p.log.Info("pushplay: match finished", "match", matchID, "status", v.Status, "fallbacks", fallbacks)
			// Lifecycle: /game-end with a FAT, replayable result — the outcome plus
			// the full round-by-round history, so the agent has the complete match
			// record without stitching /event notifications together.
			result, _ := json.Marshal(gameEndResult{
				Result:  v.Result,
				MatchID: matchID,
				Game:    "goofspiel",
				Seat:    0,
				History: historyFromView(v),
			})
			p.emitGameEnd(ctx, agentID, target, matchID, result)
			return
		}
		if !v.YourTurn {
			// House is acting (rare — it plays inline in commit); yield briefly.
			time.Sleep(150 * time.Millisecond)
			continue
		}

		legal := v.LegalActions.PlayCardFrom
		if len(legal) == 0 {
			legal = v.You.Hand
		}
		if len(legal) == 0 {
			p.log.Warn("pushplay: no legal action available, stopping", "match", matchID)
			return
		}

		card, usedFallback := p.decide(ctx, target, matchID, v, legal)
		if usedFallback {
			fallbacks++
		}
		if _, err := p.driver.Act(ctx, agentID, matchID, v.Round, card, ""); err != nil {
			p.log.Warn("pushplay: submit failed, stopping driver", "match", matchID, "round", v.Round, "err", err)
			return
		}
	}
}

// decide asks the remote endpoint for a card and validates it against the legal
// set, falling back to the lowest legal card on any error or illegal response.
// dispatchRoundEvents pushes a /event webhook for each round that resolved since
// the last read. Delivery is async (a goroutine per event) so the turn loop and
// the engine never block on it; each event carries Seq so the agent can order
// them, and agentclient retries + signs. Returns the new highest delivered round.
func (p *pushPlayer) dispatchRoundEvents(ctx context.Context, agentID string, target agentclient.Target, matchID string, v match.AgentView, delivered int) int {
	highest := delivered
	for _, h := range v.History {
		if h.Round <= delivered {
			continue
		}
		payload, _ := json.Marshal(h)
		p.emitEvent(ctx, agentID, target, matchID, h.Round, "round_revealed", payload)
		if h.Round > highest {
			highest = h.Round
		}
	}
	return highest
}

// emitEvent / emitGameEnd persist the notification to the durable webhook queue
// (delivered by the central dispatcher: signed, retried, health-gated). Without a
// queue configured they fall back to a best-effort detached goroutine so the loop
// and engine still never block. Detached context: persistence/delivery must
// outlive the match's drive deadline.
func (p *pushPlayer) emitEvent(ctx context.Context, agentID string, target agentclient.Target, matchID string, seq int, eventType string, payload []byte) {
	if p.enqueue != nil {
		if err := p.enqueue.EnqueueEvent(context.WithoutCancel(ctx), agentID, "goofspiel", matchID, seq, eventType, payload); err != nil {
			p.log.Warn("pushplay: enqueue event failed", "match", matchID, "err", err)
		}
		return
	}
	n := agentclient.EventNotification{MatchID: matchID, Game: "goofspiel", Seq: seq, Type: eventType, Payload: payload}
	go func() { _ = p.client.Event(context.WithoutCancel(ctx), target, n) }()
}

func (p *pushPlayer) emitGameEnd(ctx context.Context, agentID string, target agentclient.Target, matchID string, result []byte) {
	if p.enqueue != nil {
		if err := p.enqueue.EnqueueGameEnd(context.WithoutCancel(ctx), agentID, "goofspiel", matchID, result); err != nil {
			p.log.Warn("pushplay: enqueue game-end failed", "match", matchID, "err", err)
		}
		return
	}
	if err := p.client.GameEnd(context.WithoutCancel(ctx), target, agentclient.GameEndNotification{
		MatchID: matchID, Game: "goofspiel", Result: result,
	}); err != nil {
		p.log.Warn("pushplay: game-end delivery failed", "match", matchID, "err", err)
	}
}

func (p *pushPlayer) decide(ctx context.Context, target agentclient.Target, matchID string, v match.AgentView, legal []int) (int, bool) {
	view := remoteplay.GoofspielView{
		Game:         "goofspiel",
		MatchID:      matchID,
		Seat:         0, // developer is always seat A in a sandbox match
		Round:        v.Round,
		CurrentPrize: v.CurrentPrize,
		PrizePool:    v.PrizePool,
		YourHand:     v.You.Hand,
		Scores:       [2]int{v.You.Score, v.Opponent.Score},
		LegalActions: legal,
		History:      historyFromView(v), // self-contained: every resolved round so far
	}
	var move remoteplay.GoofspielMove
	if _, err := p.client.Play(ctx, target, view, &move); err == nil && containsInt(legal, move.Card) {
		return move.Card, false
	}
	return lowestInt(legal), true
}

// historyFromView maps the match service's per-round history into the seat's
// replayable RoundView list (developer is seat 0). Running scores are computed
// cumulatively so the payload is a complete, self-consistent record.
func historyFromView(v match.AgentView) []remoteplay.RoundView {
	out := make([]remoteplay.RoundView, 0, len(v.History))
	cum := [2]int{}
	for _, h := range v.History {
		w := winnerSeat(h.Winner)
		if w == 0 {
			cum[0] += h.PrizePool
		} else if w == 1 {
			cum[1] += h.PrizePool
		}
		out = append(out, remoteplay.RoundView{
			Round:     h.Round,
			Prize:     h.Prize,
			PrizePool: h.PrizePool,
			YourCard:  h.YourCard,
			OppCard:   h.OppCard,
			Winner:    w,
			Scores:    cum,
		})
	}
	return out
}

// gameEndResult is the fat /game-end payload: the settled result plus the full
// replayable round history. The embedded Result keeps the historical shape
// (winner + coins) so existing consumers keep working; History is additive.
type gameEndResult struct {
	Result  any                    `json:"result"`
	MatchID string                 `json:"match_id"`
	Game    string                 `json:"game"`
	Seat    int                    `json:"seat"`
	History []remoteplay.RoundView `json:"history"`
}

// winnerSeat maps the match view's "you"/"opponent"/"tie" to a seat index from
// the developer's perspective (seat 0), matching remoteplay.RoundView semantics.
func winnerSeat(winner string) int {
	switch winner {
	case "you":
		return 0
	case "opponent":
		return 1
	default:
		return -1 // tie
	}
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func lowestInt(xs []int) int {
	m := xs[0]
	for _, x := range xs {
		if x < m {
			m = x
		}
	}
	return m
}
