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
}

// NewHandler builds the wallet handler. allowMint gates POST /v1/admin/mint; it
// must be false in prod/staging (Stage 5 supplies real top-ups).
func NewHandler(svc *Service, authn *auth.Authenticator, allowMint bool) *Handler {
	return &Handler{svc: svc, authn: authn, allowMint: allowMint}
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
			r.Post("/v1/admin/mint", h.mint)
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
	lines, err := h.svc.History(r.Context(), agentID, limit)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"transactions": lines})
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
	lines, err := h.svc.UserHistory(r.Context(), p.UserPublicID, limit)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"transactions": lines})
}

func (h *Handler) allocate(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Agent  string `json:"agent"`
		Amount int64  `json:"amount"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if err := h.authorizeFor(r, p, in.Agent); err != nil {
		httpx.Error(w, err)
		return
	}
	if err := h.svc.Allocate(r.Context(), p.UserPublicID, in.Agent, in.Amount); err != nil {
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
	p := auth.PrincipalFromContext(r.Context())
	if err := h.authorizeFor(r, p, in.Agent); err != nil {
		httpx.Error(w, err)
		return
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
	if target == "" {
		return "", httpx.NewError(http.StatusBadRequest, "agent_required", "Specify ?agent= for a user-scoped token.")
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
