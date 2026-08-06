package pindex

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Publishing the P-Index method, and versioning it the way documentation is versioned.
//
// The public endpoint is deliberately UNAUTHENTICATED. A reputation number that a
// developer cannot check the method for is a number they are asked to trust; publishing
// the method is what makes it a number they can argue with. Every serious index does this
// — the h-index, impact factors, credit scoring — and the ones that do not are the ones
// nobody believes.
//
// Nothing here is editorial. The weights come from the ACTIVE config row and the formulas
// come from each dimension's own Explain method, next to the code that scores it, so the
// published page cannot describe a formula the engine is not running. Prose, worked
// examples and changelog live in the docs system, which is separately versioned.

// ConfigStore is the admin write side: list every version, add one, and choose which is
// live. Implemented by store.PIndexRepo.
type ConfigStore interface {
	ActiveConfig(ctx context.Context) (Config, error)
	// ListConfigs returns every version, newest first, with its active flag.
	ListConfigs(ctx context.Context) ([]VersionedConfig, error)
	// PutConfig writes a version's raw params. Rejected if the weights do not sum to 1.0.
	PutConfig(ctx context.Context, version int, params []byte) error
	// ActivateConfig makes exactly one version live, atomically.
	ActivateConfig(ctx context.Context, version int) error
}

// VersionedConfig is one stored scoring config.
type VersionedConfig struct {
	Version int    `json:"version"`
	Active  bool   `json:"active"`
	Params  []byte `json:"params"`
}

// Handler serves the public methodology and the admin config surface.
type Handler struct {
	store  ConfigStore
	engine *Engine
	authn  *auth.Authenticator
	admins map[string]bool
}

func NewHandler(store ConfigStore, engine *Engine, authn *auth.Authenticator, adminUserIDs []string) *Handler {
	admins := make(map[string]bool, len(adminUserIDs))
	for _, id := range adminUserIDs {
		admins[id] = true
	}
	return &Handler{store: store, engine: engine, authn: authn, admins: admins}
}

func (h *Handler) Register(r chi.Router) {
	// PUBLIC. No auth: the method behind a public score has to be publicly checkable.
	r.Get("/v1/pindex/methodology", h.methodology)

	guard := auth.RequirePlatformOrAdmin(h.admins)
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.With(guard).Get("/v1/admin/pindex/configs", h.listConfigs)
		r.With(guard).Put("/v1/admin/pindex/configs", h.putConfig)
		r.With(guard).Post("/v1/admin/pindex/configs/activate", h.activate)
	})
}

// methodology serves the published method for the ACTIVE config.
func (h *Handler) methodology(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.store.ActiveConfig(r.Context())
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, Describe(cfg, h.engine.Dimensions()))
}

func (h *Handler) listConfigs(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.ListConfigs(r.Context())
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"configs": out})
}

// putConfig writes a version's params.
//
// Validated through ParseConfig before it is stored, so a config whose weights do not sum
// to 1.0 is rejected at the boundary rather than discovered when it silently produces
// scores above the scale. A published index must not be able to enter an incoherent state
// through its own admin surface.
func (h *Handler) putConfig(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version int             `json:"version"`
		Params  json.RawMessage `json:"params"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.Version <= 0 || len(in.Params) == 0 {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "invalid_input",
			"version (positive) and params are required"))
		return
	}
	if _, err := ParseConfig(in.Version, in.Params); err != nil {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "invalid_config", err.Error()))
		return
	}
	if err := h.store.PutConfig(r.Context(), in.Version, in.Params); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"version": in.Version, "stored": true})
}

// activate makes one version live.
//
// Separate from putConfig on purpose, mirroring how a doc version is written and then
// published: writing a candidate must never change what developers are being scored on,
// because a P-Index change re-ranks everyone at once. Two steps means the re-rank is always
// a decision somebody made rather than a side effect of an edit.
func (h *Handler) activate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version int `json:"version"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.Version <= 0 {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "invalid_input", "version is required"))
		return
	}
	if err := h.store.ActivateConfig(r.Context(), in.Version); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"version": in.Version, "active": true})
}
