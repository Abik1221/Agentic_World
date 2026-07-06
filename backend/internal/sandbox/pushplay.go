package sandbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/remoteplay"
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
	log    *slog.Logger
	// maxMatch caps a single driven match's wall-clock so a stalled/hostile
	// endpoint can never leak a goroutine forever.
	maxMatch time.Duration
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

	fallbacks := 0
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
		if v.Status != "active" {
			p.log.Info("pushplay: match finished", "match", matchID, "status", v.Status, "fallbacks", fallbacks)
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
	}
	var move remoteplay.GoofspielMove
	if _, err := p.client.Play(ctx, target, view, &move); err == nil && containsInt(legal, move.Card) {
		return move.Card, false
	}
	return lowestInt(legal), true
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
