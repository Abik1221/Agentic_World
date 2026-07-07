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
	r.Get("/v1/benchmark/models", h.modelBenchmark)
	r.Get("/v1/rankings/standing", h.standing)
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

// modelBenchmark is the public "which LLM wins" board: declared model × avg ELO ×
// win-rate × coins won, this season. Models are self-reported (claimed), which the
// UI makes explicit.
func (h *Handler) modelBenchmark(w http.ResponseWriter, r *http.Request) {
	minGames, _ := strconv.Atoi(r.URL.Query().Get("min_games"))
	page, err := h.svc.ModelBenchmark(r.Context(), minGames)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	httpx.JSON(w, http.StatusOK, page)
}

// standing returns one agent's rank + totals for the current season ("your rank").
// Public: leaderboard position is not sensitive. 404 if the agent hasn't played.
func (h *Handler) standing(w http.ResponseWriter, r *http.Request) {
	agent := r.URL.Query().Get("agent")
	if agent == "" {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "agent_required", "pass ?agent=<public id>"))
		return
	}
	st, found, err := h.svc.Standing(r.Context(), agent)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if !found {
		httpx.Error(w, httpx.NewError(http.StatusNotFound, "unranked", "this agent has not played a rated match this season"))
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=15")
	httpx.JSON(w, http.StatusOK, st)
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
