package rating

import (
	"net/http"
	"strconv"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler serves the public leaderboard. allowDevRoll gates a dev-only
// force-roll endpoint (off in prod) used to exercise the season champion surface.
type Handler struct {
	svc          *Service
	allowDevRoll bool
}

func NewHandler(svc *Service, allowDevRoll bool) *Handler {
	return &Handler{svc: svc, allowDevRoll: allowDevRoll}
}

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/leaderboard", h.leaderboard)
	r.Get("/v1/seasons/current", h.currentSeason)
	r.Get("/v1/seasons/champion", h.seasonChampion)
	if h.allowDevRoll {
		r.Post("/v1/admin/dev/roll-season", h.devRollSeason)
	}
}

// devRollSeason finalises the current season now (dev only) so the season champion
// can be observed without waiting for a real season boundary.
func (h *Handler) devRollSeason(w http.ResponseWriter, r *http.Request) {
	season, champion, err := h.svc.ForceRollCurrentSeason(r.Context())
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"rolled_season": season, "champion_agent": champion})
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
