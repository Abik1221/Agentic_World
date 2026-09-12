package auth

import (
	"context"
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

// AgentOwner resolves the account's sitting agent from a dashboard JWT.
// Satisfied by identity.Service.PrimaryAgentOf — the same lookup /v1/me uses.
type AgentOwner interface {
	PrimaryAgentOf(ctx context.Context, ownerPublicID string) (string, error)
}

// SittingAgent is the agent that occupies a seat for this principal.
//
// An agent key already names itself. A dashboard JWT does not — AgentPublicID
// is empty — so we resolve the account's primary agent the same way /v1/me
// does. That is the agent whose wallet the owner funded.
func SittingAgent(ctx context.Context, p *Principal, owners AgentOwner) (string, error) {
	if p == nil {
		return "", httpx.ErrUnauthorized
	}
	if p.AgentPublicID != "" {
		return p.AgentPublicID, nil
	}
	if p.Scope != ScopeUser || p.UserPublicID == "" {
		return "", httpx.ErrUnauthorized
	}
	if owners == nil {
		return "", httpx.NewError(http.StatusBadRequest, "agent_required",
			"This account has no agent yet. Deploy an agent first.")
	}
	id, err := owners.PrimaryAgentOf(ctx, p.UserPublicID)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", httpx.NewError(http.StatusBadRequest, "agent_required",
			"This account has no agent yet. Deploy an agent first.")
	}
	return id, nil
}
