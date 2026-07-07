package antifraud

import (
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes the user-facing dispute report and the admin review/resolve
// endpoints. Admin access is an explicit allowlist of user public ids.
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
		// Admin authorization is enforced at the router (Platform token or an
		// allowlisted user) so it can't be forgotten in a handler.
		admin := auth.RequirePlatformOrAdmin(h.admins)
		r.With(user).Post("/v1/disputes", h.report)
		r.With(admin).Post("/v1/admin/disputes/{id}/resolve", h.resolve)
		r.With(admin).Get("/v1/admin/agent/{id}/timing", h.timing)
	})
}

func (h *Handler) report(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Match  string `json:"match"`
		Agent  string `json:"agent"`
		Kind   string `json:"kind"`
		Detail string `json:"detail"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.Kind == "" {
		in.Kind = "other"
	}
	id, err := h.svc.ReportDispute(r.Context(), p.UserPublicID, in.Match, in.Agent, in.Kind, in.Detail)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"dispute_id": id})
}

func (h *Handler) resolve(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if !h.isAdmin(p) {
		httpx.Error(w, httpx.ErrForbidden)
		return
	}
	var in struct {
		Action string `json:"action"` // refund | release | reject
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	switch in.Action {
	case "refund", "release", "reject":
	default:
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "invalid_action", "action must be refund, release, or reject."))
		return
	}
	if err := h.svc.ResolveDispute(r.Context(), p.UserPublicID, chi.URLParam(r, "id"), in.Action); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"resolved": true, "action": in.Action})
}

func (h *Handler) timing(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if !h.isAdmin(p) {
		httpx.Error(w, httpx.ErrForbidden)
		return
	}
	stat, likelihood, human, err := h.svc.AgentTiming(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"samples": stat.Count, "mean_ms": stat.MeanMs, "std_ms": stat.StdMs,
		"human_likelihood": likelihood, "looks_human": human,
	})
}

func (h *Handler) isAdmin(p *auth.Principal) bool {
	return auth.IsAdmin(p, h.admins)
}
