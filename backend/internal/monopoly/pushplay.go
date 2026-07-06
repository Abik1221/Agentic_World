package monopoly

import (
	"context"
	"log/slog"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
	mono "github.com/agent-arena/arena/internal/engine/monopoly"
	"github.com/agent-arena/arena/internal/httpx"
)

// This file adds push-play to Monopoly: the platform drives the developer's seat
// (seat 0) of a no-stakes table by calling their hosted agent endpoint (manifest
// push model) for each decision, while the engine's deterministic bots fill and
// play the remaining seats. It reuses the exact same match machinery — the
// browser watches live over the normal SSE/state endpoints.

// RemoteResolver returns the push-play Target for an agent's active, verified
// manifest. Implemented by manifest.Service.
type RemoteResolver interface {
	PlayTarget(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error)
}

// PushClient POSTs a game view to the developer's endpoint and decodes the move.
// Implemented by *agentclient.Client.
type PushClient interface {
	Play(ctx context.Context, t agentclient.Target, request, out any) (int, error)
}

type pushPlayer struct {
	remote   RemoteResolver
	client   PushClient
	log      *slog.Logger
	maxMatch time.Duration
}

// EnablePushPlay wires the optional POST /v1/monopoly/pushplay capability.
func (s *Service) EnablePushPlay(remote RemoteResolver, client PushClient, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	s.pusher = &pushPlayer{remote: remote, client: client, log: log, maxMatch: 5 * time.Minute}
}

// MonopolyPushView is the JSON the platform POSTs to the agent endpoint each turn.
// It carries the redacted board (future cards stripped), the legal action kinds,
// and which seat the agent holds.
type MonopolyPushView struct {
	Game         string      `json:"game"` // "monopoly"
	MatchID      string      `json:"match_id"`
	Seat         int         `json:"seat"`
	Phase        string      `json:"phase"`
	LegalActions []string    `json:"legal_actions"`
	State        *mono.State `json:"state"`
}

// MonopolyPushMove is the action the agent returns.
type MonopolyPushMove struct {
	Action   string `json:"action"`
	Property int    `json:"property"`
	Amount   int    `json:"amount"`
}

// StartPushPlay opens a no-stakes Monopoly table (creator seat 0 + engine bots)
// and drives seat 0 from the developer's hosted endpoint. Requires an active,
// endpoint-verified manifest.
func (s *Service) StartPushPlay(ctx context.Context, agentPublicID, ownerPublicID string, players int) (string, error) {
	if s.pusher == nil {
		return "", httpx.NewError(501, "pushplay_unavailable", "Push-play is not configured on this server.")
	}
	target, found, err := s.pusher.remote.PlayTarget(ctx, agentPublicID)
	if err != nil {
		return "", err
	}
	if !found || target.EndpointURL == "" {
		return "", httpx.NewError(400, "no_verified_endpoint",
			"Register and verify your agent's endpoint (manifest) before running push-play.")
	}
	id, err := s.CreateTable(ctx, agentPublicID, ownerPublicID, 0, players)
	if err != nil {
		return "", err
	}
	go s.pusher.drive(s, id, agentPublicID, target)
	return id, nil
}

// drive polls for seat 0's turn and asks the endpoint for each action until the
// match ends. An error/timeout/illegal move falls back to a safe legal action so
// a bad endpoint can never wedge the match.
func (p *pushPlayer) drive(s *Service, matchID, agentID string, target agentclient.Target) {
	ctx, cancel := context.WithTimeout(context.Background(), p.maxMatch)
	defer cancel()

	fallbacks := 0
	for {
		if ctx.Err() != nil {
			p.log.Warn("monopoly pushplay: deadline exceeded", "match", matchID)
			return
		}
		v, err := s.State(ctx, matchID, agentID, false, 0)
		if err != nil {
			p.log.Warn("monopoly pushplay: state read failed", "match", matchID, "err", err)
			return
		}
		if v.Status != StatusActive {
			p.log.Info("monopoly pushplay: match finished", "match", matchID, "status", v.Status, "fallbacks", fallbacks)
			return
		}
		if !v.YourTurn || len(v.Legal) == 0 {
			time.Sleep(150 * time.Millisecond)
			continue
		}

		act, usedFallback := p.decide(ctx, target, matchID, v)
		if usedFallback {
			fallbacks++
		}
		if _, err := s.Act(ctx, agentID, matchID, act); err != nil {
			// The chosen action was rejected; try a guaranteed-safe fallback once.
			if _, err2 := s.Act(ctx, agentID, matchID, safeFallback(v.Legal)); err2 != nil {
				p.log.Warn("monopoly pushplay: submit failed", "match", matchID, "err", err2)
				return
			}
			fallbacks++
		}
	}
}

func (p *pushPlayer) decide(ctx context.Context, target agentclient.Target, matchID string, v AgentView) (mono.Action, bool) {
	req := MonopolyPushView{
		Game: "monopoly", MatchID: matchID, Seat: v.YourSeat,
		Phase: v.Phase, LegalActions: v.Legal, State: v.State,
	}
	var move MonopolyPushMove
	if _, err := p.client.Play(ctx, target, req, &move); err == nil && containsStr(v.Legal, move.Action) {
		return mono.Action{Kind: move.Action, Property: move.Property, Amount: move.Amount}, false
	}
	return safeFallback(v.Legal), true
}

// safeFallback returns a legal action that needs no extra parameters where
// possible (roll/end_turn/decline/pass/…), else the first legal kind. Mirrors the
// engine's own missed-window default so the match always advances.
func safeFallback(legal []string) mono.Action {
	targetless := map[string]bool{
		"roll": true, "roll_jail": true, "pay_jail": true, "use_jail_card": true,
		"end_turn": true, "decline": true, "pass": true, "bankrupt": true,
		"accept_trade": true, "reject_trade": true,
	}
	// Prefer the least-committal safe actions first.
	for _, pref := range []string{"end_turn", "decline", "pass", "roll", "roll_jail", "reject_trade"} {
		if containsStr(legal, pref) {
			return mono.Action{Kind: pref}
		}
	}
	for _, k := range legal {
		if targetless[k] {
			return mono.Action{Kind: k}
		}
	}
	if len(legal) > 0 {
		return mono.Action{Kind: legal[0]}
	}
	return mono.Action{Kind: "end_turn"}
}

func containsStr(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
