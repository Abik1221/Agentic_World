package growthstats

import (
	"net/http"
	"strconv"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler serves Super-Admin growth analytics reads over the signed platform bus.
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
	guard := auth.RequirePlatformOrAdmin(h.admins)
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.With(guard).Get("/v1/admin/analytics/growth/summary", h.summary)
		r.With(guard).Get("/v1/admin/analytics/growth/timeseries", h.timeseries)
		r.With(guard).Get("/v1/admin/analytics/growth/funnel", h.funnel)
		r.With(guard).Get("/v1/admin/analytics/growth/auth", h.authMix)
		r.With(guard).Get("/v1/admin/analytics/growth/countries", h.countries)
		r.With(guard).Get("/v1/admin/analytics/growth/visitors", h.visitors)
	})
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	s, err := h.svc.Summary(r.Context(), r.URL.Query().Get("range"))
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "analytics_unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

func (h *Handler) timeseries(w http.ResponseWriter, r *http.Request) {
	label, rows, err := h.svc.Timeseries(r.Context(), r.URL.Query().Get("range"))
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "analytics_unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"range": label, "series": rows})
}

func (h *Handler) funnel(w http.ResponseWriter, r *http.Request) {
	label, f, c, err := h.svc.Funnel(r.Context(), r.URL.Query().Get("range"))
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "analytics_unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"range": label, "funnel": f, "conversion": c, "definitions": definitions(),
	})
}

func (h *Handler) authMix(w http.ResponseWriter, r *http.Request) {
	label, rows, err := h.svc.Auth(r.Context(), r.URL.Query().Get("range"))
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "analytics_unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"range": label, "auth_mix": rows})
}

func (h *Handler) countries(w http.ResponseWriter, r *http.Request) {
	page := atoiDefault(r.URL.Query().Get("page"), 1)
	pageSize := atoiDefault(r.URL.Query().Get("page_size"), 25)
	label, rows, total, p, size, err := h.svc.Countries(r.Context(), r.URL.Query().Get("range"), page, pageSize)
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "analytics_unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"range": label, "countries": rows, "total": total, "page": p, "page_size": size,
		"note": "Prefers signup GeoIP country; falls back to self-reported profile country.",
	})
}

func (h *Handler) visitors(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, h.svc.Visitors(r.Context(), r.URL.Query().Get("range")))
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
