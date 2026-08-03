package wallet

import (
	"net/http"
	"strconv"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/go-chi/chi/v5"
)

// Handler serves the read-only wallet surface and the non-prod mint affordance.
type Handler struct {
	svc       *Service
	authn     *auth.Authenticator
	allowMint bool
	admins    map[string]bool
}

// NewHandler builds the wallet handler. allowMint gates POST /v1/admin/mint; it
// must be false in prod/staging (Stage 5 supplies real top-ups).
func NewHandler(svc *Service, authn *auth.Authenticator, allowMint bool, adminUserIDs []string) *Handler {
	admins := make(map[string]bool, len(adminUserIDs))
	for _, id := range adminUserIDs {
		admins[id] = true
	}
	return &Handler{svc: svc, authn: authn, allowMint: allowMint, admins: admins}
}

// Register mounts the wallet routes (all require a valid credential).
func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.Get("/v1/wallet", h.get)
		r.Get("/v1/wallet/history", h.history)
		r.Get("/v1/user/wallet", h.userSummary)
		r.Get("/v1/user/wallet/history", h.userHistory)
		r.Post("/v1/wallet/allocate", h.allocate)
		if h.allowMint {
			// Admin-enforced at the ROUTER, not just by the allowMint flag. mint only
			// checked that the caller owned the target agent, so with ALLOW_MINT on
			// (its default outside prod/staging) every registered beta tester could
			// mint themselves coins — poisoning the leaderboard, ELO, benchmark and the
			// withdrawal queue this release is meant to test.
			r.With(auth.RequirePlatformOrAdmin(h.admins)).Post("/v1/admin/mint", h.mint)
		}
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	agentID, err := h.resolveAgent(r)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	view, err := h.svc.View(r.Context(), agentID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	agentID, err := h.resolveAgent(r)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	lines, next, err := h.svc.History(r.Context(), agentID, limit, offset)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	resp := map[string]any{"transactions": lines}
	if next > 0 {
		resp["next_cursor"] = next
	}
	httpx.JSON(w, http.StatusOK, resp)
}

func (h *Handler) userSummary(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if p == nil || p.UserPublicID == "" {
		httpx.Error(w, httpx.ErrUnauthorized)
		return
	}
	sum, err := h.svc.UserSummary(r.Context(), p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, sum)
}

func (h *Handler) userHistory(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if p == nil || p.UserPublicID == "" {
		httpx.Error(w, httpx.ErrUnauthorized)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	lines, next, err := h.svc.UserHistory(r.Context(), p.UserPublicID, limit, offset)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	resp := map[string]any{"transactions": lines}
	if next > 0 {
		resp["next_cursor"] = next
	}
	httpx.JSON(w, http.StatusOK, resp)
}

func (h *Handler) allocate(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Agent   string `json:"agent"`
		Amount  int64  `json:"amount"`
		IdemKey string `json:"idempotency_key"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if err := h.authorizeFor(r, p, in.Agent); err != nil {
		httpx.Error(w, err)
		return
	}
	// Idempotency key: body field, else the standard header. When present, a
	// retried allocate is a no-op instead of double-moving the owner's treasury.
	idemKey := in.IdemKey
	if idemKey == "" {
		idemKey = r.Header.Get("Idempotency-Key")
	}
	if err := h.svc.Allocate(r.Context(), p.UserPublicID, in.Agent, in.Amount, idemKey); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"agent": in.Agent, "allocated": in.Amount})
}

func (h *Handler) mint(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Agent  string `json:"agent"`
		Amount int64  `json:"amount"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.Amount <= 0 {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "invalid_amount", "Amount must be positive."))
		return
	}
	// Per-call ceiling: mint is a non-prod test affordance (the route is only mounted
	// when ALLOW_MINT is on, which is forced off in prod), but cap the amount anyway so a
	// stray/huge value can't create an absurd balance or approach int64 overflow.
	// Defense in depth.
	const maxMintPerCall int64 = 10_000_000
	if in.Amount > maxMintPerCall {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "invalid_amount", "Amount exceeds the per-call mint ceiling."))
		return
	}
	p := auth.PrincipalFromContext(r.Context())
	// An ADMIN or PLATFORM caller does not have to own the agent; anyone else does.
	//
	// This route's guard is RequirePlatformOrAdmin, and the handler then also demanded that
	// the caller personally own the target agent — which a Platform token, being a
	// service-to-service credential belonging to no developer, never can. So the endpoint
	// was unreachable by exactly the credential it exists for: every call came back 403.
	// (The E2E cert-gate test died on it, and the money-flow test skipped on it with a
	// message blaming the wrong thing, which is how it stayed hidden.)
	//
	// Nothing is loosened by this. The protections that matter are both still in force and
	// both sit in front of this line: the route is not mounted at all unless ALLOW_MINT is
	// on (forced off in prod by config.Validate), and the guard admits only a verified
	// Platform token or a user on the ADMIN_USER_IDS allowlist. A self-registered developer
	// still cannot reach this handler to credit anybody, including themselves — which is
	// the property the ownership check was reaching for and the guard already guarantees.
	if !auth.IsAdmin(p, h.admins) {
		if err := h.authorizeFor(r, p, in.Agent); err != nil {
			httpx.Error(w, err)
			return
		}
	}
	if err := h.svc.Mint(r.Context(), in.Agent, in.Amount, platform.NewID("mint")); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"agent": in.Agent, "minted": in.Amount})
}

// resolveAgent picks the target agent from the principal (agent scope) or the
// ?agent= query (user scope) and verifies the caller owns it.
func (h *Handler) resolveAgent(r *http.Request) (string, error) {
	p := auth.PrincipalFromContext(r.Context())
	if p == nil {
		return "", httpx.ErrUnauthorized
	}
	target := r.URL.Query().Get("agent")
	if target == "" {
		target = p.AgentPublicID
	}
	// A dashboard token carries no agent id, so a browser had to supply ?agent= — and the
	// only place the browser got it from was a cookie written at login. Any session that did
	// not come through that exact path (a cleared cookie, a refresh-token restore, a second
	// device) sent no agent, got this 400, and the Strategy page rendered every guardrail as
	// zero and could not save. The owner's own agent is something the server knows; asking
	// the client to tell us was the mistake.
	//
	// Mirrors payout.Service.Available, which has resolved the primary agent this way all
	// along — the two endpoints backing the same screens should not disagree about whether
	// the caller has to name their agent.
	if target == "" && p.UserPublicID != "" {
		primary, err := h.svc.PrimaryAgent(r.Context(), p.UserPublicID)
		if err != nil {
			return "", err
		}
		target = primary
	}
	if target == "" {
		return "", httpx.NewError(http.StatusBadRequest, "agent_required",
			"This account has no agent yet. Create one, or pass ?agent= explicitly.")
	}
	if err := h.authorizeFor(r, p, target); err != nil {
		return "", err
	}
	return target, nil
}

// authorizeFor confirms the principal's user owns agentPublicID. An agent
// credential whose own agent is the target short-circuits; otherwise the
// owner is looked up and compared. A missing agent reads as not-found so we
// never reveal which ids exist.
func (h *Handler) authorizeFor(r *http.Request, p *auth.Principal, agentPublicID string) error {
	if p == nil {
		return httpx.ErrUnauthorized
	}
	if p.AgentPublicID == agentPublicID && agentPublicID != "" {
		return nil
	}
	owner, err := h.svc.OwnerOf(r.Context(), agentPublicID)
	if err != nil {
		return httpx.ErrNotFound
	}
	if owner != p.UserPublicID {
		return httpx.ErrForbidden
	}
	return nil
}
