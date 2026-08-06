package llmgw

import (
	"context"
	"net/http"
	"strings"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// HTTP surface for the gateway.
//
// # The onboarding constraint that shaped this
//
// A verified tier is worth nothing if nobody routes through it, and the friction that
// stops developers is not the concept — it is being told to restructure their code. Every
// major provider SDK already supports overriding a base URL, so the entire ask should be
// ONE line:
//
//	client = OpenAI(base_url="https://api.pyyol.com/v1/gw/openai/v1", api_key=my_key)
//	client = Anthropic(base_url="https://api.pyyol.com/v1/gw/anthropic", api_key=my_key)
//
// Everything after the provider slug is forwarded verbatim, so the provider's own SDK
// keeps constructing its own paths, versions and payloads. We proxy bytes, not semantics —
// which also means a provider shipping a new endpoint tomorrow needs no change here.
//
// # Why the provider is in the PATH and not inferred
//
// Inferring the provider from the model name ("claude-*" → Anthropic) reads as friendlier
// and is a trap: it silently guesses, it breaks the moment a provider serves another
// vendor's model (Bedrock, Vertex, OpenRouter all do), and a wrong guess sends a
// developer's API KEY to the wrong company. An explicit slug is one extra path segment and
// removes a whole class of credential-leak bug.
//
// A convenience `auto` slug is offered for the genuinely common case, but it routes only on
// an unambiguous prefix and refuses rather than guessing.
type Handler struct {
	gw    *Gateway
	authn *auth.Authenticator
}

func NewHandler(gw *Gateway, authn *auth.Authenticator) *Handler {
	return &Handler{gw: gw, authn: authn}
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		// Agent scope: the caller is an agent playing a match, identified by its Pyyol API
		// key. That identity is the one thing in this flow the agent cannot assert — every
		// other field is either forwarded verbatim or made trustworthy by the turn proof.
		gate := auth.RequireScope(auth.ScopeAgent)
		// Wildcard on every method a provider might use. The gateway is a byte pipe: it
		// must not need to know which verbs an endpoint supports.
		r.With(gate).Handle("/v1/gw/{provider}/*", http.HandlerFunc(h.proxy))
		// Coverage: how much of an agent's play is actually verified. Readable by the agent
		// itself so a developer can see their own verified share before it appears on a
		// public board — a badge you discover you have lost is a support ticket.
		r.With(gate).Get("/v1/gw/coverage", h.coverage)
	})
}

// CoverageReader reports an agent's verified share. Implemented by store.LLMGatewayRepo.
type CoverageReader interface {
	CoverageFor(ctx context.Context, agentPublicID, matchID string) (Coverage, error)
}

// Coverage is how much of an agent's play was actually verified.
//
// THE number a verified tier stands on. A badge meaning "routed at least one call through
// us" is decoration; one meaning "94% of this agent's decisions were proven LLM-backed" is
// a claim. Publishing the denominator is also what stops a cost-per-win board rewarding an
// agent for routing LESS.
//
// Declared here rather than in the store so the gateway does not depend on the database
// layer that happens to persist it.
type Coverage struct {
	AgentPublicID  string  `json:"agent_public_id"`
	Decisions      int     `json:"decisions"`
	BoundDecisions int     `json:"bound_decisions"`
	Coverage       float64 `json:"coverage"`
}

func (h *Handler) proxy(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	provider := chi.URLParam(r, "provider")
	rest := chi.URLParam(r, "*")

	if provider == "auto" {
		resolved, ok := h.resolveProvider(r)
		if !ok {
			httpx.Error(w, httpx.NewError(http.StatusBadRequest, "ambiguous_provider",
				"Could not determine the provider from the model name. Name it in the path "+
					"instead, e.g. /v1/gw/anthropic/… — we refuse to guess because a wrong "+
					"guess would send your API key to the wrong company."))
			return
		}
		provider = resolved
	}
	h.gw.Proxy(w, r, p.AgentPublicID, provider, "/"+rest)
}

// resolveProvider maps an unambiguous model prefix onto a provider.
//
// Refuses on anything it does not recognise with certainty. The failure mode of guessing
// here is sending a developer's credential to a company they did not choose, so "I am not
// sure" must be an error rather than a best effort.
func (h *Handler) resolveProvider(r *http.Request) (string, bool) {
	// Read the hint the SDK sets, before falling back to the model name. An explicit
	// header beats a prefix heuristic whenever the SDK knows.
	if p := strings.ToLower(strings.TrimSpace(r.Header.Get(HeaderProvider))); p != "" {
		if _, ok := h.gw.cfg.Upstreams[p]; ok {
			return p, true
		}
		return "", false
	}
	return "", false
}

func (h *Handler) coverage(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if h.gw.coverage == nil {
		httpx.Error(w, httpx.NewError(http.StatusNotImplemented, "coverage_unavailable",
			"Coverage reporting is not configured on this deployment."))
		return
	}
	// Scoped to the CALLER. An agent reading another agent's verified share would be a
	// competitive intelligence leak — coverage reveals how much someone routes and
	// therefore how they built their agent.
	out, err := h.gw.coverage.CoverageFor(r.Context(), p.AgentPublicID, r.URL.Query().Get("match"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
