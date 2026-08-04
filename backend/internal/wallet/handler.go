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

		// AN AGENT'S PLAYING WALLET — agent or owner, and both are legitimate.
		//
		// The SDK reads its own balance with an agent key (resolveAgent falls back to
		// p.AgentPublicID), and the dashboard reads it for a chosen agent with ?agent= on a
		// user token. RequireScopeAny states that pair instead of leaving it to bare authn,
		// which also admitted a Platform service token — an admin credential had a silent
		// side door into per-agent balances that no admin surface asks for.
		agentOrOwner := auth.RequireScopeAny(auth.ScopeAgent, auth.ScopeUser)
		r.With(agentOrOwner).Get("/v1/wallet", h.get)
		r.With(agentOrOwner).Get("/v1/wallet/history", h.history)

		// THE OWNER'S TREASURY — user scope, enforced at the router.
		//
		// These two had no scope guard, and their handlers only check that the principal
		// carries a UserPublicID. That is an IDENTITY check, not an authorization one: an
		// agent API key resolves to its OWNER's UserPublicID, so every agent key could read
		// its owner's treasury balance and their entire ledger — deposits, withdrawals,
		// allocations, match settlements. The caller's own money, in the sense that the key
		// belongs to their agent; not the caller's own decision, in the sense that an agent
		// is a program deployed to a container or a CI runner and its key is expected to
		// leak eventually.
		//
		// This is the allocate bug's shape exactly, in the same file: a handler doing its own
		// ownership reasoning on a router that declared no scope. allocate got the guard
		// because it MOVED money; these were left because they only READ it. But the reason
		// allocate needed the guard was never that it wrote — it was that an agent key is not
		// the owner, and that is equally true of a read. Disclosure is not the smaller half
		// of a compromise when what is disclosed is a complete financial history.
		//
		// Nothing legitimate loses access. The dashboard sends session.dashboardToken and
		// `pyyol wallet` sends the CLI's access_token; both are user-scope, and the CLI
		// already refuses to run this command without one. No SDK path reads either route.
		owner := auth.RequireScope(auth.ScopeUser)
		r.With(owner).Get("/v1/user/wallet", h.userSummary)
		r.With(owner).Get("/v1/user/wallet/history", h.userHistory)
		// USER SCOPE, enforced at the router.
		//
		// allocate moves coins out of the OWNER's treasury into an agent's playing wallet.
		// It had no scope guard, and authorizeFor() returns nil as soon as the principal's
		// agent id matches the target — so an AGENT-scope key was accepted, and an agent
		// key resolves to its owner's UserPublicID. A key that leaked from a CI runner or a
		// container could therefore drain its owner's entire treasury into the agent
		// wallet, in one call, with no cap.
		//
		// Nothing legitimate did that. No SDK calls this route; the only caller is the
		// dashboard's AllocateModal, which sends session.dashboardToken, and the client's
		// own comment on allocateToAgent already says "(user)". Funding an agent is an
		// OWNER decision — the agent needs coins in its wallet, not the authority to put
		// them there.
		//
		// Same fix as /v1/admin/mint below, for the same reason: enforced at the router so
		// authz cannot be forgotten by a future handler edit. With the scope guard, no cap
		// is needed — the caller is the owner moving their own money, and it still cannot
		// leave the platform without a withdrawal, which requires user scope AND 2FA.
		r.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/wallet/allocate", h.allocate)
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
