package gamestakes

import (
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler serves the game stake-tier surface: a PUBLIC read of the enabled tiers
// (the menu a client/agent picks from) and Super-Admin GET/PUT to configure them,
// authorized by an Ed25519 Platform token OR the ADMIN_USER_IDS allowlist (same
// guard as adminapi/walletadmin). The admin UI (separate repo) drives PUT.
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
	// Public: the enabled tier menu for a game (client UI + agents choose a tier).
	r.Get("/v1/games/{game}/stakes", h.publicList)

	// Super-Admin: read the full set (incl. disabled) + replace it.
	guard := auth.RequirePlatformOrAdmin(h.admins)
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.With(guard).Get("/v1/admin/games/{game}/stakes", h.adminGet)
		r.With(guard).Put("/v1/admin/games/{game}/stakes", h.adminPut)
	})
}

func actor(r *http.Request) string {
	if p := auth.PrincipalFromContext(r.Context()); p != nil && p.UserPublicID != "" {
		return p.UserPublicID
	}
	return "platform"
}

func (h *Handler) publicList(w http.ResponseWriter, r *http.Request) {
	tiers, err := h.svc.List(r.Context(), chi.URLParam(r, "game"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=10")
	httpx.JSON(w, http.StatusOK, map[string]any{"game": chi.URLParam(r, "game"), "tiers": tiers})
}

func (h *Handler) adminGet(w http.ResponseWriter, r *http.Request) {
	gt, err := h.svc.AdminGet(r.Context(), chi.URLParam(r, "game"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, gt)
}

func (h *Handler) adminPut(w http.ResponseWriter, r *http.Request) {
	game := chi.URLParam(r, "game")
	var in struct {
		Tiers []Tier `json:"tiers"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if err := h.svc.AdminPut(r.Context(), actor(r), game, in.Tiers); err != nil {
		httpx.Error(w, err)
		return
	}
	gt, err := h.svc.AdminGet(r.Context(), game)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, gt)
}
