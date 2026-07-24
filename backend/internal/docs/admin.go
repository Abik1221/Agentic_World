package docs

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// AdminStore is the write side of docs-as-data (implemented by store.DocsRepo). It
// lets a super-admin edit/publish doc versions from the console without a redeploy;
// the git-seeded baseline version is untouched unless explicitly edited.
type AdminStore interface {
	Store
	UpsertPage(ctx context.Context, version string, p Page) error
	DeletePage(ctx context.Context, version, slug string) (bool, error)
	CloneVersion(ctx context.Context, from, to string) (int, error)
	ListFull(ctx context.Context, version string) ([]Page, error)
}

// AdminHandler serves the Super-Admin docs CRUD (/v1/admin/docs/*), gated by an
// Ed25519 platform token OR the ADMIN_USER_IDS allowlist — the same guard as the
// other admin surfaces. The admin UI (separate repo) drives it.
type AdminHandler struct {
	store  AdminStore
	authn  *auth.Authenticator
	admins map[string]bool
}

func NewAdminHandler(store AdminStore, authn *auth.Authenticator, adminUserIDs []string) *AdminHandler {
	admins := make(map[string]bool, len(adminUserIDs))
	for _, id := range adminUserIDs {
		admins[id] = true
	}
	return &AdminHandler{store: store, authn: authn, admins: admins}
}

func (h *AdminHandler) Register(r chi.Router) {
	guard := auth.RequirePlatformOrAdmin(h.admins)
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.With(guard).Get("/v1/admin/docs", h.listFull)                // ?version= (full pages w/ bodies)
		r.With(guard).Post("/v1/admin/docs/versions", h.createVersion) // {version, from?}
		r.With(guard).Put("/v1/admin/docs/pages", h.upsertPage)        // {version, slug, ...body}
		r.With(guard).Delete("/v1/admin/docs/pages", h.deletePage)     // ?version=&slug=
	})
}

func (h *AdminHandler) resolveVersion(ctx context.Context, req string) (string, error) {
	if req != "" {
		return req, nil
	}
	_, latest, err := h.store.Versions(ctx)
	return latest, err
}

func (h *AdminHandler) listFull(w http.ResponseWriter, r *http.Request) {
	version, err := h.resolveVersion(r.Context(), r.URL.Query().Get("version"))
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "docs_unavailable"})
		return
	}
	pages, err := h.store.ListFull(r.Context(), version)
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "docs_unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"version": version, "pages": pages})
}

func (h *AdminHandler) createVersion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version string `json:"version"`
		From    string `json:"from"` // optional: clone all pages from this version
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]any{"error": "bad_json"})
		return
	}
	in.Version = strings.TrimSpace(in.Version)
	if in.Version == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string]any{"error": "version_required"})
		return
	}
	copied := 0
	if in.From != "" {
		n, err := h.store.CloneVersion(r.Context(), in.From, in.Version)
		if err != nil {
			httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "clone_failed"})
			return
		}
		copied = n
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"version": in.Version, "cloned_from": in.From, "pages": copied})
}

func (h *AdminHandler) upsertPage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version  string `json:"version"`
		Slug     string `json:"slug"`
		Title    string `json:"title"`
		Section  string `json:"section"`
		Game     string `json:"game"`
		Category string `json:"category"`
		Order    int    `json:"order"`
		Body     string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.JSON(w, http.StatusBadRequest, map[string]any{"error": "bad_json"})
		return
	}
	in.Version, in.Slug = strings.TrimSpace(in.Version), strings.TrimSpace(in.Slug)
	if in.Version == "" || in.Slug == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string]any{"error": "version_and_slug_required"})
		return
	}
	p := Page{
		Slug: in.Slug, Title: in.Title, Section: in.Section, Game: in.Game,
		Category: in.Category, Order: in.Order, Body: in.Body,
	}
	if err := h.store.UpsertPage(r.Context(), in.Version, p); err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "upsert_failed"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"version": in.Version, "slug": in.Slug, "ok": true})
}

func (h *AdminHandler) deletePage(w http.ResponseWriter, r *http.Request) {
	version := strings.TrimSpace(r.URL.Query().Get("version"))
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if version == "" || slug == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string]any{"error": "version_and_slug_required"})
		return
	}
	deleted, err := h.store.DeletePage(r.Context(), version, slug)
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "delete_failed"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"version": version, "slug": slug, "deleted": deleted})
}
