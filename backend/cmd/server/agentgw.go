package main

import (
	"context"
	"crypto/subtle"
	"log/slog"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/agentgw"
)

// secretResolver is the slice of manifest.Service the socket authenticator needs:
// the plaintext endpoint secret for an agent. It is the same sealed secret the
// developer sets with `onavion publish` (PUT .../endpoint-secret), so a local
// agent authenticates its socket with the credential it already has — no new
// key management. (OAuth access tokens are the Phase D upgrade.)
type secretResolver interface {
	PlayTarget(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error)
}

// socketAuthenticator validates a register frame's (agent_id, token) against the
// agent's stored endpoint secret with a constant-time compare.
type socketAuthenticator struct {
	resolver secretResolver
	log      *slog.Logger
}

func (a socketAuthenticator) Authenticate(ctx context.Context, token, agentID string) (string, bool) {
	if agentID == "" || token == "" {
		return "", false
	}
	target, found, err := a.resolver.PlayTarget(ctx, agentID)
	if err != nil {
		a.log.Warn("agentgw: auth lookup failed", "agent", agentID, "err", err)
		return "", false
	}
	if !found || target.Token == "" {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(target.Token)) == 1 {
		return agentID, true
	}
	return "", false
}

// newAgentGateway builds the WSS agent gateway wired to the manifest secret store.
func newAgentGateway(resolver secretResolver, log *slog.Logger) *agentgw.Gateway {
	return agentgw.New(socketAuthenticator{resolver: resolver, log: log}, agentgw.Options{}, log)
}
