package clips

import (
	"net/http"
	"strconv"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler serves the public trending-clips feed.
type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/clips/trending", h.trending)
}

func (h *Handler) trending(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	clips, err := h.svc.Trending(r.Context(), limit, offset)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	next := ""
	if limit > 0 && len(clips) == limit {
		next = strconv.Itoa(offset + limit)
	}
	w.Header().Set("Cache-Control", "public, max-age=15")
	httpx.JSON(w, http.StatusOK, map[string]any{"clips": clips, "next_cursor": next})
}
