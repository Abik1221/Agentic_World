package main

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/agentgw"
	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
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

// mountAgentStatus serves GET /v1/agent/status?agent_id=… — is my agent connected
// right now (for `onavion status` and the dashboard card). User-scoped like the
// other agent routes. Returns online/offline plus SDK/games/last-seen when live.
func mountAgentStatus(authn *auth.Authenticator, gw *agentgw.Gateway) func(chi.Router) {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(authn.Middleware)
			r.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/agent/status", func(w http.ResponseWriter, req *http.Request) {
				agentID := req.URL.Query().Get("agent_id")
				if agentID == "" {
					httpx.JSON(w, http.StatusBadRequest, map[string]any{"error": "agent_id required"})
					return
				}
				st, online := gw.ConnectionStatus(agentID)
				out := map[string]any{"agent_id": agentID, "online": online}
				if online {
					out["name"] = st.Name
					out["games"] = st.Games
					out["sdk_version"] = st.SDKVersion
					out["last_seen"] = st.LastSeen.UTC().Format("2006-01-02T15:04:05Z")
				}
				httpx.JSON(w, http.StatusOK, out)
			})
		})
	}
}
