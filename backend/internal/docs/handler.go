package docs

import (
	"context"
	"net/http"
	"sort"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Store is the persistence the handler reads (implemented by store.DocsRepo).
type Store interface {
	ListPages(ctx context.Context, version string) ([]Page, error)
	GetPage(ctx context.Context, version, slug string) (Page, bool, error)
	Versions(ctx context.Context) (versions []string, latest string, err error)
}

// Handler serves the public docs API consumed by the frontend docs UI.
type Handler struct{ store Store }

func NewHandler(store Store) *Handler { return &Handler{store: store} }

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/docs", h.tree)              // nav tree (metadata) for a version
	r.Get("/v1/docs/versions", h.versions) // available versions + latest (matched before the wildcard)
	r.Get("/v1/docs/*", h.page)            // one full page; the wildcard captures nested slugs (games/goofspiel)
}

// navPage is the metadata shape in the nav tree (no body).
type navPage struct {
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	Game     string `json:"game,omitempty"`
	Category string `json:"category,omitempty"`
	Order    int    `json:"order"`
}

type navSection struct {
	Section string    `json:"section"`
	Pages   []navPage `json:"pages"`
}

// resolveVersion returns the requested ?version, or the latest when unset/blank.
func (h *Handler) resolveVersion(ctx context.Context, req string) (string, error) {
	if req != "" {
		return req, nil
	}
	_, latest, err := h.store.Versions(ctx)
	return latest, err
}

func (h *Handler) tree(w http.ResponseWriter, r *http.Request) {
	version, err := h.resolveVersion(r.Context(), r.URL.Query().Get("version"))
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "docs_unavailable"})
		return
	}
	pages, err := h.store.ListPages(r.Context(), version)
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "docs_unavailable"})
		return
	}
	// Group into sections. Pages arrive ordered by (ord, slug) — correct WITHIN a
	// section — but the DB can't know the section nav ranking, so sort the sections
	// themselves by docs.SectionRank afterward (Getting Started → SDK → Games → …).
	var sections []navSection
	idx := map[string]int{}
	for _, p := range pages {
		i, ok := idx[p.Section]
		if !ok {
			i = len(sections)
			idx[p.Section] = i
			sections = append(sections, navSection{Section: p.Section})
		}
		sections[i].Pages = append(sections[i].Pages, navPage{
			Slug: p.Slug, Title: p.Title, Game: p.Game, Category: p.Category, Order: p.Order,
		})
	}
	sort.SliceStable(sections, func(a, b int) bool {
		return SectionRank(sections[a].Section) < SectionRank(sections[b].Section)
	})
	w.Header().Set("Cache-Control", "public, max-age=60")
	httpx.JSON(w, http.StatusOK, map[string]any{"version": version, "sections": sections})
}

func (h *Handler) page(w http.ResponseWriter, r *http.Request) {
	// The wildcard captures the full nested slug (e.g. "games/goofspiel").
	slug := chi.URLParam(r, "*")
	if slug == "" {
		httpx.JSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
		return
	}
	version, err := h.resolveVersion(r.Context(), r.URL.Query().Get("version"))
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "docs_unavailable"})
		return
	}
	p, ok, err := h.store.GetPage(r.Context(), version, slug)
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "docs_unavailable"})
		return
	}
	if !ok {
		httpx.JSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "slug": slug})
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"version": version, "slug": p.Slug, "title": p.Title, "section": p.Section,
		"game": p.Game, "category": p.Category, "order": p.Order, "body": p.Body,
	})
}

func (h *Handler) versions(w http.ResponseWriter, r *http.Request) {
	versions, latest, err := h.store.Versions(r.Context())
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"error": "docs_unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"versions": versions, "latest": latest})
}
