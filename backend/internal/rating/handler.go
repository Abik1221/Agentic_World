package rating

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler serves the public leaderboard. allowDevRoll gates a dev-only
// force-roll endpoint (off in prod) used to exercise the season champion surface.
type Handler struct {
	svc          *Service
	authn        *auth.Authenticator
	allowDevRoll bool
	admins       map[string]bool
}

func NewHandler(svc *Service, authn *auth.Authenticator, allowDevRoll bool, adminUserIDs []string) *Handler {
	admins := make(map[string]bool, len(adminUserIDs))
	for _, id := range adminUserIDs {
		admins[id] = true
	}
	return &Handler{svc: svc, authn: authn, allowDevRoll: allowDevRoll, admins: admins}
}

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/leaderboard", h.leaderboard)
	r.Get("/v1/seasons/current", h.currentSeason)
	r.Get("/v1/seasons/champion", h.seasonChampion)
	r.Get("/v1/benchmark/models", h.modelBenchmark)
	// The PLATFORM harness board's operational stats: thinking time, reasoning tokens and
	// cost per decision, over matches Pyyol ran itself. A literal path rather than a
	// parameter on the line above, because the public route surface is pinned by a scan for
	// literal route strings and a computed one would be invisible to it.
	r.Get("/v1/benchmark/harness/models", h.harnessBenchmark)
	// The matches BEHIND the numbers, for the public clips page.
	r.Get("/v1/benchmark/harness/matches", h.harnessMatches)
	r.Get("/v1/benchmark/model", h.modelDetail)
	r.Get("/v1/benchmark/developers", h.developerBoard)
	r.Get("/v1/rankings/standing", h.standing)
	if h.allowDevRoll {
		// Force-rolling the season was mounted on the PUBLIC router with no auth at
		// all: any anonymous caller could finalize the season and stamp a champion,
		// destroying the leaderboard integrity this release is meant to validate.
		// Now authenticated AND admin-only, like every other /v1/admin route.
		r.Group(func(r chi.Router) {
			r.Use(h.authn.Middleware)
			r.With(auth.RequirePlatformOrAdmin(h.admins)).
				Post("/v1/admin/dev/roll-season", h.devRollSeason)
		})
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

// modelBenchmark is the public "which LLM wins" board for the season: outcomes,
// token economics, wall-clock and move quality per model, aggregated across every
// arena by default (?game=<arena> narrows it, with the per-arena breakdown always
// attached). Each row states the tier its model attribution came from.
func (h *Handler) modelBenchmark(w http.ResponseWriter, r *http.Request) {
	game := r.URL.Query().Get("game")
	// An unrecognised arena is rejected rather than answered with an empty board: a
	// typo that renders as "no models have played" is indistinguishable from the truth.
	if !IsArena(game) {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "unknown_arena",
			"game must be one of: all, "+strings.Join(Arenas, ", ")))
		return
	}
	minGames, _ := strconv.Atoi(r.URL.Query().Get("min_games"))
	page, err := h.svc.ModelBenchmark(r.Context(), game, minGames)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	httpx.JSON(w, http.StatusOK, page)
}

// harnessBenchmark mirrors modelBenchmark over the platform's own benchmark matches.
func (h *Handler) harnessBenchmark(w http.ResponseWriter, r *http.Request) {
	game := r.URL.Query().Get("game")
	if !IsArena(game) {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "unknown_arena",
			"game must be one of: all, "+strings.Join(Arenas, ", ")))
		return
	}
	minGames, _ := strconv.Atoi(r.URL.Query().Get("min_games"))
	page, err := h.svc.HarnessBenchmark(r.Context(), game, minGames)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	httpx.JSON(w, http.StatusOK, page)
}

// harnessMatches lists published platform-harness matches that can be replayed.
//
// Cached for five minutes rather than thirty seconds: this list changes only when the
// platform runs new harness matches, and the page it feeds is a browse surface. A short
// TTL here would put a database scan behind every visitor for data that is effectively
// static between benchmark runs.
func (h *Handler) harnessMatches(w http.ResponseWriter, r *http.Request) {
	game := r.URL.Query().Get("game")
	if game == "" {
		game = ArenaAll
	}
	if !IsArena(game) {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "unknown_arena",
			"game must be one of: all, "+strings.Join(Arenas, ", ")))
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	matches, err := h.svc.HarnessMatches(r.Context(), game, limit)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	httpx.JSON(w, http.StatusOK, map[string]any{"matches": matches, "game": game})
}

// modelDetail is one model's page: its full season record, the per-arena split, and
// the agents running it.
//
// Provider and model arrive as QUERY parameters rather than path segments because real
// model ids contain slashes and colons ("meta-llama/llama-3.3-70b-instruct",
// "llama3.3:70b-instruct-q4_K_M"). Putting them in the path would need double-encoding
// that proxies and CDNs normalize away, and the id would silently break.
func (h *Handler) modelDetail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	provider, model := q.Get("provider"), q.Get("model")
	if model == "" {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "model_required",
			"pass ?provider=<provider>&model=<model>"))
		return
	}
	game := q.Get("game")
	if !IsArena(game) {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "unknown_arena",
			"game must be one of: all, "+strings.Join(Arenas, ", ")))
		return
	}
	detail, found, err := h.svc.ModelDetail(r.Context(), game, provider, model)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if !found {
		httpx.Error(w, httpx.NewError(http.StatusNotFound, "model_not_found",
			"no finished match this season was played by that model"))
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	httpx.JSON(w, http.StatusOK, detail)
}

// developerBoard is the public AGENTIC benchmark: who gets the most out of the model
// they run. Public for the same reason the model board is — it is the platform's claim
// about itself, and a leaderboard nobody outside can read is a marketing asset, not a
// benchmark.
func (h *Handler) developerBoard(w http.ResponseWriter, r *http.Request) {
	game := r.URL.Query().Get("game")
	if !IsArena(game) {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "unknown_arena",
			"game must be one of: all, "+strings.Join(Arenas, ", ")))
		return
	}
	minGames, _ := strconv.Atoi(r.URL.Query().Get("min_games"))
	board, err := h.svc.DeveloperBoard(r.Context(), game, minGames)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	httpx.JSON(w, http.StatusOK, board)
}

// standing returns one agent's rank + totals for the current season ("your rank").
// Public: leaderboard position is not sensitive. 404 if the agent hasn't played.
// Without ?game=, it answers for the agent's most-played arena and names it in the
// response, rather than assuming one arena and reporting "unranked" for the rest.
func (h *Handler) standing(w http.ResponseWriter, r *http.Request) {
	agent := r.URL.Query().Get("agent")
	if agent == "" {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "agent_required", "pass ?agent=<public id>"))
		return
	}
	game := r.URL.Query().Get("game")
	if !IsArena(game) {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "unknown_arena",
			"game must be one of: all, "+strings.Join(Arenas, ", ")))
		return
	}
	st, found, err := h.svc.Standing(r.Context(), agent, game)
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

	page, err := h.svc.Leaderboard(r.Context(), r.URL.Query().Get("game"), season, offset, limit)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=10") // read-path scaling via CDN/replica
	httpx.JSON(w, http.StatusOK, page)
}
