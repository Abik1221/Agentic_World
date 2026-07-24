package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/llmgateway"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/go-chi/chi/v5"
)

// mountLLMGateway builds the Pyyol LLM Gateway proxy and registers it at /gw/*.
//
// Auth: the X-Pyyol-Key header is validated against the SAME agent-key resolver the
// socket gateway uses (a login-issued ScopeAgent key resolved to its owning agent
// id). It only IDENTIFIES the agent — the developer's own provider key rides
// Authorization and is forwarded to the LLM provider untouched.
func mountLLMGateway(keys keyResolver, em *telemetry.Client, verified llmgateway.VerifiedHook, log *slog.Logger) httpx.Mount {
	authenticate := func(ctx context.Context, rawKey string) (string, bool) {
		if keys == nil || rawKey == "" {
			return "", false
		}
		p, err := keys.ResolveAgentKey(ctx, rawKey)
		if err != nil || p == nil || p.Scope != auth.ScopeAgent || p.AgentPublicID == "" {
			return "", false
		}
		return p.AgentPublicID, true
	}
	proxy := llmgateway.New(em, log,
		llmgateway.WithAuthenticator(authenticate),
		llmgateway.WithVerifiedHook(verified),
	)
	return func(r chi.Router) {
		// Mounted at /gw; strip it so the proxy sees /openai/... or /anthropic/...
		r.Handle("/gw/*", http.StripPrefix("/gw", proxy))
	}
}
