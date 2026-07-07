package payout

import (
	"net/http"
	"strconv"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes the cash-out surface: withdrawable quote + request (user) and
// approve/reject (admin allowlist).
type Handler struct {
	svc    *Service
	authn  *auth.Authenticator
	admins map[string]bool
}

func NewHandler(svc *Service, authn *auth.Authenticator, adminUserIDs []string) *Handler {
	admins := make(map[string]bool, len(adminUserIDs))
	for _, id := range adminUserIDs {
		admins[id] = true
	}
	return &Handler{svc: svc, authn: authn, admins: admins}
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		user := auth.RequireScope(auth.ScopeUser)
		// `get` is owner-or-admin (the handler checks ownership), so it admits any
		// logged-in user + the Platform token. The /v1/admin/* actions are
		// admin-only and enforced at the router so authz can't be forgotten.
		ownerOrAdmin := auth.RequireScopeAny(auth.ScopeUser, auth.ScopePlatform)
		adminOnly := auth.RequirePlatformOrAdmin(h.admins)
		r.With(user).Get("/v1/wallet/withdrawable", h.withdrawable)
		r.With(user).Get("/v1/withdrawals", h.list)
		r.With(user).Post("/v1/withdrawals", h.request)
		r.With(ownerOrAdmin).Get("/v1/withdrawals/{id}", h.get)
		r.With(adminOnly).Get("/v1/admin/withdrawals", h.adminList)
		r.With(adminOnly).Post("/v1/admin/withdrawals/{id}/approve", h.approve)
		r.With(adminOnly).Post("/v1/admin/withdrawals/{id}/reject", h.reject)
	})
}

func (h *Handler) withdrawable(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	agent := r.URL.Query().Get("agent")
	if agent == "" {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "agent_required", "Specify ?agent="))
		return
	}
	var coins int64
	if q := r.URL.Query().Get("coins"); q != "" {
		if n, err := strconv.ParseInt(q, 10, 64); err == nil {
			coins = n
		}
	}
	avail, quote, err := h.svc.Available(r.Context(), p.UserPublicID, agent, coins)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"withdrawable_coins": avail, "quote": quote})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := h.svc.List(r.Context(), p.UserPublicID, limit)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"withdrawals": items})
}

func (h *Handler) adminList(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if !h.isAdmin(p) {
		httpx.Error(w, httpx.ErrForbidden)
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "requested"
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := h.svc.AdminQueue(r.Context(), status, limit)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"withdrawals": items})
}

func (h *Handler) request(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Agent string `json:"agent"`
		Coins int64  `json:"coins"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	wd, err := h.svc.Request(r.Context(), p.UserPublicID, in.Agent, in.Coins)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, wd)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	wd, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if wd.Owner != p.UserPublicID && !auth.IsAdmin(p, h.admins) {
		httpx.Error(w, httpx.ErrForbidden)
		return
	}
	httpx.JSON(w, http.StatusOK, wd)
}

func (h *Handler) approve(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if !h.isAdmin(p) {
		httpx.Error(w, httpx.ErrForbidden)
		return
	}
	if err := h.svc.Approve(r.Context(), p.UserPublicID, chi.URLParam(r, "id")); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "paid"})
}

func (h *Handler) reject(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if !h.isAdmin(p) {
		httpx.Error(w, httpx.ErrForbidden)
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	_ = httpx.DecodeJSON(w, r, &in)
	if err := h.svc.Reject(r.Context(), p.UserPublicID, chi.URLParam(r, "id"), in.Reason); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "rejected"})
}

func (h *Handler) isAdmin(p *auth.Principal) bool {
	return auth.IsAdmin(p, h.admins)
}
