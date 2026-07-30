package main

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/agentgw"
	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/middleware"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/platformcfg"
	"github.com/go-chi/chi/v5"
)

// secretResolver is the slice of manifest.Service the socket authenticator needs:
// the plaintext endpoint secret for an agent (the sealed secret set with
// `pyyol publish`).
type secretResolver interface {
	PlayTarget(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error)
}

// keyResolver maps a raw agent API key to its Principal (implemented by identity).
// This is the credential `pyyol login` obtains via the dashboard /cli-login page.
type keyResolver interface {
	ResolveAgentKey(ctx context.Context, rawKey string) (*auth.Principal, error)
}

// socketAuthenticator validates a register frame's (agent_id, token). It accepts
// either credential the developer may hold:
//   - an **agent API key** (ScopeAgent) — the primary path; issued to the CLI by
//     the dashboard /cli-login flow and resolved to its owning agent id;
//   - the agent's **manifest endpoint secret** — the fallback for an agent that
//     published a hosted endpoint and reuses that secret for the socket.
//
// Either must resolve to the claimed agent id.
type socketAuthenticator struct {
	resolver secretResolver
	keys     keyResolver
	log      *slog.Logger
}

func (a socketAuthenticator) Authenticate(ctx context.Context, token, agentID string) (string, bool) {
	if agentID == "" || token == "" {
		return "", false
	}
	// 1. Agent API key (ScopeAgent) — the login-issued credential.
	if a.keys != nil {
		if p, err := a.keys.ResolveAgentKey(ctx, token); err == nil && p != nil &&
			p.Scope == auth.ScopeAgent && p.AgentPublicID == agentID {
			return agentID, true
		}
	}
	// 2. Manifest endpoint secret (constant-time compare) — the publish fallback.
	if target, found, err := a.resolver.PlayTarget(ctx, agentID); err == nil && found && target.Token != "" {
		if subtle.ConstantTimeCompare([]byte(token), []byte(target.Token)) == 1 {
			return agentID, true
		}
	} else if err != nil {
		a.log.Warn("agentgw: auth lookup failed", "agent", agentID, "err", err)
	}
	return "", false
}

// newAgentGateway builds the WSS agent gateway wired to the agent-key resolver
// (login credential) and the manifest secret store (publish fallback). When a
// platform-config provider is supplied, the gateway learns the latest/minimum
// SDK version per language (live) for the upgrade nudge + too-old refusal.
func newAgentGateway(resolver secretResolver, keys keyResolver, cfg *platformcfg.Provider, em *telemetry.Client, log *slog.Logger, reconnectGrace time.Duration) *agentgw.Gateway {
	opts := agentgw.Options{ClientIP: middleware.ClientIP, Emitter: em, ReconnectGrace: reconnectGrace}
	if cfg != nil {
		opts.SDKVersionInfo = func(language string) (latest, min string) {
			sdk := cfg.Get().SDK
			return sdk.LatestSDKVersions[language], sdk.MinSDKVersions[language]
		}
	}
	return agentgw.New(socketAuthenticator{resolver: resolver, keys: keys, log: log}, opts, log)
}

// mountCapabilities exposes a public GET /v1/config so the frontend can hide flows
// that are disabled in this environment (X-claim onboarding, Solana deposits)
// instead of letting the user hit a 503/404. Booleans only — never any secret or
// key material.
func mountCapabilities(xClaim, deposits, devMode bool) func(chi.Router) {
	return func(r chi.Router) {
		r.Get("/v1/config", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=30")
			httpx.JSON(w, http.StatusOK, map[string]any{
				"onboarding_x_claim": xClaim,   // /register + /verify usable
				"deposits":           deposits, // /v1/deposits usable
				"dev_mode":           devMode,
			})
		})
	}
}

// mountAgentStatus serves GET /v1/agent/status?agent_id=… — is my agent connected
// right now (for `pyyol status` and the dashboard card). User-scoped like the
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
