package sdkstats

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler serves the public install-ping ingest + the Super-Admin analytics reads.
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
	// Public: the SDK pings this on first run (no auth — it's anonymous telemetry).
	r.Post("/v1/telemetry/install", h.ingest)

	// Super-Admin analytics.
	guard := auth.RequirePlatformOrAdmin(h.admins)
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.With(guard).Get("/v1/admin/analytics/sdk/summary", h.summary)
		r.With(guard).Get("/v1/admin/analytics/sdk/timeseries", h.timeseries)
		r.With(guard).Get("/v1/admin/analytics/sdk/countries", h.countries)
	})
}

// ingest records one first-run ping. Country comes from the CDN's CF-IPCountry header
// (Cloudflare) when present — we resolve to a COUNTRY and never store the raw IP.
func (h *Handler) ingest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SDK     string `json:"sdk"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]any{"error": "bad_json"})
		return
	}
	country := NormalizeCountry(r.Header.Get("CF-IPCountry"))
	if err := h.svc.RecordInstall(r.Context(), in.SDK, in.Version, country); err != nil {
		if errors.Is(err, ErrBadSDK) {
			httpx.JSON(w, http.StatusBadRequest, map[string]any{"error": "unknown_sdk"})
			return
		}
		// Analytics must never fail the caller loudly; swallow storage errors.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	s, err := h.svc.Summary(r.Context())
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "analytics_unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

func (h *Handler) timeseries(w http.ResponseWriter, r *http.Request) {
	gran := r.URL.Query().Get("granularity")
	rows, err := h.svc.Timeseries(r.Context(), gran)
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "analytics_unavailable"})
		return
	}
	if rows == nil {
		rows = []TimeseriesRow{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"granularity": gran, "series": rows})
}

func (h *Handler) countries(w http.ResponseWriter, r *http.Request) {
	page := atoiDefault(r.URL.Query().Get("page"), 1)
	pageSize := atoiDefault(r.URL.Query().Get("page_size"), 25)
	rows, total, p, size, err := h.svc.Countries(r.Context(), page, pageSize)
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "analytics_unavailable"})
		return
	}
	if rows == nil {
		rows = []CountryCount{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"countries": rows, "total": total, "page": p, "page_size": size,
	})
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
