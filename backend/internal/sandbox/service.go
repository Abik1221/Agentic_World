package sandbox

import (
	"context"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/match"
)

// Starter is the slice of match.Service the sandbox needs: open a practice match.
// Kept as an interface so the service is unit-testable with a fake.
type Starter interface {
	CreateSandbox(ctx context.Context, humanAgent, humanOwner, houseAgent, houseOwner, policy string) (string, error)
}

// Service orchestrates practice matches against the house agents.
type Service struct {
	matches Starter
	enabled bool
	// pusher is set by EnablePushPlay to enable /v1/sandbox/pushplay (the manifest
	// "push" model). Nil ⇒ push-play returns 501.
	pusher *pushPlayer
	// turns mints the per-turn proof that binds a gateway LLM call to ONE decision.
	// Nil ⇒ views ship without a proof, so no decision here can be counted as LLM-backed.
	turns TurnMinter
}

// TurnMinter issues the token that binds a gateway LLM call to one decision. Satisfied by
// *turnproof.Signer — the same signer every game uses, so one secret covers all of them and
// the gateway verifies them identically.
type TurnMinter interface {
	Mint(agentID, matchID string, round int) string
}

// SetTurnMinter wires the per-turn proof minter. Call BEFORE EnablePushPlay, which copies it
// onto the pusher.
//
// Sandbox mints proofs deliberately, matching the Mafia and Monopoly push paths. It is the
// one place a developer can confirm their gateway wiring actually earns credit before any
// coins are at stake; without a proof here they would discover a broken Verified setup only
// in ranked play, where it costs them. Sandbox binding carries no reward on its own — the
// integrity gate reads ranked matches — so this buys confidence, not standing.
func (s *Service) SetTurnMinter(m TurnMinter) { s.turns = m }

// New constructs the sandbox service. When enabled is false, Start returns a 403
// so the feature can be turned off per-environment without removing the routes.
func New(matches Starter, enabled bool) *Service {
	return &Service{matches: matches, enabled: enabled}
}

// Opponents returns the selectable house roster (easy → hard).
func (s *Service) Opponents() []Opponent { return catalog }

// Start opens a sandbox match between the developer's agent and the house agent
// for the chosen difficulty. No coins, no limits, no rating — see
// match.Service.CreateSandbox. The dev then plays the standard match endpoints.
func (s *Service) Start(ctx context.Context, humanAgent, humanOwner, difficulty string) (StartResult, error) {
	if !s.enabled {
		return StartResult{}, httpx.NewError(403, "sandbox_disabled", "Sandbox practice mode is disabled in this environment.")
	}
	opp := opponentFor(difficulty)
	id, err := s.matches.CreateSandbox(ctx, humanAgent, humanOwner, opp.ID, HouseOwner, opp.Style)
	if err != nil {
		return StartResult{}, err
	}
	return StartResult{MatchID: id, Mode: match.ModeSandbox, Opponent: opp}, nil
}
