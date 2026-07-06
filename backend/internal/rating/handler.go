package rating

import (
	"net/http"
	"strconv"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler serves the public leaderboard.
type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/leaderboard", h.leaderboard)
	r.Get("/v1/seasons/current", h.currentSeason)
	r.Get("/v1/seasons/champion", h.seasonChampion)
}

func (h *Handler) currentSeason(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=30")
	httpx.JSON(w, http.StatusOK, h.svc.CurrentSeasonInfo())
}

// seasonChampion returns the winner of the most recently finalised season, with
// full stats + avatar (null champion until a season has rolled).
func (h *Handler) seasonChampion(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.SeasonChampion(r.Context())
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) leaderboard(w http.ResponseWriter, r *http.Request) {
	season, _ := strconv.Atoi(r.URL.Query().Get("season"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	page, err := h.svc.Leaderboard(r.Context(), season, offset, limit)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=10") // read-path scaling via CDN/replica
	httpx.JSON(w, http.StatusOK, page)
}
