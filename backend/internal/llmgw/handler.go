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
// major provider SDK already supports overriding a base URL, so the ask stays small:
//
//	client = Anthropic(
//	    api_key=my_provider_key,                       # still YOUR key, passed through
//	    base_url="https://api.pyyol.com/v1/gw/anthropic",
//	    default_headers={"X-Pyyol-Key": my_pyyol_key}, # who is calling
//	)
//
// or, with the SDK doing it for you and adding the per-turn proof:
//
//	client = pyyol.route(Anthropic())
//
// The Pyyol key CANNOT ride in the standard auth header, and this is worth stating plainly
// because an earlier draft of this file claimed a one-line base_url change was enough. It is
// not: that slot is already occupied by the provider's own credential (Anthropic reads
// x-api-key, OpenAI reads "Authorization: Bearer"), which must reach the upstream untouched.
// A live agent proved the point by getting 401s from every call.
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
		r.Use(h.identify)
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

// identify resolves the caller from X-Pyyol-Key, falling back to a Bearer credential.
//
// This replaces auth.Middleware for gateway routes because Middleware reads Authorization,
// and on a gateway request that header belongs to the PROVIDER: an OpenAI-bound call carries
// the developer's OpenAI key there and it has to arrive upstream unchanged. Consuming it
// would have meant either rejecting the developer's own credential as an invalid Pyyol key
// or overwriting it and breaking the call.
//
// X-Pyyol-Key is checked FIRST and wins outright. Only when it is absent does Authorization
// get tried, which keeps `curl -H "Authorization: Bearer sk_arena_..."` working for someone
// exploring the gateway by hand without a provider SDK in the way.
//
// Like auth.Middleware this authenticates but does not authorize: an unresolvable credential
// is a 401 here, and the absence of any credential is left to RequireScope, so the failure a
// developer sees names the right problem.
func (h *Handler) identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimSpace(r.Header.Get(HeaderKey))
		if raw == "" {
			// No Pyyol identity header: fall back to Authorization, but only if it looks like
			// a Pyyol agent key. Treating an arbitrary Bearer token as one would turn a
			// developer's OpenAI key into a 401 that blames the wrong credential.
			if b := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(b, "Bearer ") {
				if tok := strings.TrimSpace(strings.TrimPrefix(b, "Bearer ")); strings.HasPrefix(tok, "sk_arena_") {
					raw = tok
				}
			}
		}
		if raw == "" {
			next.ServeHTTP(w, r) // unauthenticated; RequireScope produces the error
			return
		}
		p, err := h.authn.ResolveCredential(r.Context(), raw)
		if err != nil {
			httpx.Error(w, httpx.NewError(http.StatusUnauthorized, "pyyol_unauthorized",
				"The X-Pyyol-Key header is missing or invalid. This is your PYYOL agent key, "+
					"not your model provider key — the provider credential stays in the header "+
					"its own SDK uses and is passed through untouched."))
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), p)))
	})
}
