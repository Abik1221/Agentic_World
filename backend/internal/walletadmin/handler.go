package walletadmin

import (
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler serves the Super Admin wallet-control routes, authorized by an Ed25519
// Platform token OR the ADMIN_USER_IDS allowlist (same guard as adminapi).
type Handler struct {
	svc    *Service
	authn  *auth.Authenticator
	admins map[string]bool
}

// NewHandler builds the wallet-admin handler.
func NewHandler(svc *Service, authn *auth.Authenticator, adminUserIDs []string) *Handler {
	admins := make(map[string]bool, len(adminUserIDs))
	for _, id := range adminUserIDs {
		admins[id] = true
	}
	return &Handler{svc: svc, authn: authn, admins: admins}
}

func (h *Handler) Register(r chi.Router) {
	guard := auth.RequirePlatformOrAdmin(h.admins)
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.With(guard).Get("/v1/admin/wallet/settings", h.getSettings)
		r.With(guard).Put("/v1/admin/wallet/settings", h.updateSettings)
		r.With(guard).Post("/v1/admin/wallet/{user}/freeze", h.freeze)
		r.With(guard).Post("/v1/admin/wallet/{user}/unfreeze", h.unfreeze)
		r.With(guard).Post("/v1/admin/wallet/{user}/adjust", h.adjust)
	})
}

// actor identifies who performed the action for the audit trail.
func actor(r *http.Request) string {
	p := auth.PrincipalFromContext(r.Context())
	if p != nil && p.UserPublicID != "" {
		return p.UserPublicID
	}
	return "platform"
}

func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) {
	st, err := h.svc.Settings(r.Context())
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}

func (h *Handler) updateSettings(w http.ResponseWriter, r *http.Request) {
	var in Settings
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	st, err := h.svc.UpdateSettings(r.Context(), actor(r), in)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}

func (h *Handler) freeze(w http.ResponseWriter, r *http.Request)   { h.setFrozen(w, r, true) }
func (h *Handler) unfreeze(w http.ResponseWriter, r *http.Request) { h.setFrozen(w, r, false) }

func (h *Handler) setFrozen(w http.ResponseWriter, r *http.Request, frozen bool) {
	user := chi.URLParam(r, "user")
	if err := h.svc.SetFrozen(r.Context(), actor(r), user, frozen); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"user": user, "frozen": frozen})
}

func (h *Handler) adjust(w http.ResponseWriter, r *http.Request) {
	user := chi.URLParam(r, "user")
	var in struct {
		Coins          int64  `json:"coins"`
		Reason         string `json:"reason"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if err := h.svc.Adjust(r.Context(), actor(r), user, in.Coins, in.Reason, in.IdempotencyKey); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"user": user, "coins": in.Coins})
}
