package modelboard

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// HTTP surface for the model board.
//
// Two endpoints, and the second is not optional. A ranking of commercial models by strategic play
// is a claim strong enough that publishing the number without the method would be irresponsible —
// so the methodology is served from the SAME constants the estimator runs on, and cannot describe
// a threshold the fit is not using.
type Handler struct {
	svc     *Service
	history HistoryReader
	// boardName scopes history reads to this instance's own series. Taken from the service so a
	// handler and the service behind it can never disagree about which board they serve —
	// which would surface as a chart quietly plotting the other board's numbers.
	boardName string
}

// SetBoard names the series this handler reads. Mirrors Service.SetBoard.
func (h *Handler) SetBoard(name string) { h.boardName = name }

// HistoryReader serves one model's per-day series. Satisfied by *store.ModelBoardRepo.
type HistoryReader interface {
	BoardHistory(ctx context.Context, board, model string, since time.Time) ([]HistoryPoint, error)
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// SetHistoryReader enables the series endpoint. Optional: without it the board still serves its
// current state, there is simply no history to plot.
func (h *Handler) SetHistoryReader(r HistoryReader) { h.history = r }

// Register mounts the board under its own prefix.
//
// Parameterised because the platform harness board is the SAME handler serving a different
// instance: identical shapes, identical semantics, different data. Giving it its own prefix
// rather than a query parameter keeps the two independently cacheable, independently
// linkable, and impossible to confuse in a log.
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/benchmark/modelboard", h.board)
	r.Get("/v1/benchmark/modelboard/history", h.seriesHandler)
	r.Get("/v1/benchmark/modelboard/methodology", h.methodology)
}

// RegisterHarness mounts the SAME handlers under the platform-harness paths, for a second
// instance fitted from the platform's own benchmark matches.
//
// Written out as literals rather than built from a prefix, and that is deliberate. The
// public route surface is pinned by a test that scans this source for literal route
// strings (internal/httpx/public_routes_test.go), so a computed path is
// INVISIBLE to it — a new public route would stop being a decision point and just appear.
// A first draft of this did exactly that and silently un-pinned three existing routes.
// Three duplicated lines are cheaper than a safety control that quietly stops working.
func (h *Handler) RegisterHarness(r chi.Router) {
	r.Get("/v1/benchmark/harness", h.board)
	r.Get("/v1/benchmark/harness/history", h.seriesHandler)
	r.Get("/v1/benchmark/harness/methodology", h.methodology)
}

// seriesHandler serves one model's rating over time.
//
// Returns the INTERVAL at every point, not only the estimate. A rating line drawn without its
// uncertainty invites reading a four-point move as a change when the interval is forty points
// wide — the misreading arena boards publish a ± to prevent, which a chart can undo in one stroke
// if it plots the centre line alone.
func (h *Handler) seriesHandler(w http.ResponseWriter, r *http.Request) {
	model := r.URL.Query().Get("model")
	if model == "" {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "model_required",
			"Pass ?model=provider/name — the series is per model."))
		return
	}
	if h.history == nil {
		httpx.Error(w, httpx.NewError(http.StatusNotImplemented, "history_unavailable",
			"Rating history is not enabled on this deployment."))
		return
	}
	days := 90
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	since := time.Now().AddDate(0, 0, -days)
	points, err := h.history.BoardHistory(r.Context(), h.boardName, model, since)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"model": model,
		"days":  days,
		// Explicit, because an empty array has two meanings a chart must not conflate: a model
		// nobody has run, and a board too new to have history. The second is the current state.
		"points": points,
		// Stated on the response, not only in docs: the single most likely misreading of this
		// series is that the interval is a range the rating moved through, the way a candlestick
		// wick would be. It is not — there is ONE fit per day, and the interval is uncertainty in
		// that estimate.
		"note": "Each point is one day's fit with its 95% bootstrap interval. The interval is " +
			"uncertainty in the estimate, NOT a range the rating moved through during the day.",
	})
}

// board serves the current snapshot.
func (h *Handler) board(w http.ResponseWriter, r *http.Request) {
	snap := h.svc.Snapshot()
	if snap == nil {
		// 200 with an explicit "not computed yet", not 404 and not an empty board.
		//
		// A 404 would say the board does not exist. An empty board would say no model qualified —
		// a real and serious finding about the platform that must never be manufactured by a
		// service that simply has not finished starting up.
		httpx.JSON(w, http.StatusOK, map[string]any{
			"status": "not_computed_yet",
			"detail": "The board has not been fitted since this instance started. It refreshes on " +
				"an interval; try again shortly.",
		})
		return
	}
	httpx.JSON(w, http.StatusOK, snap)
}

// methodology describes how the number was produced, from the live configuration.
//
// Assembled from the running Config rather than written as prose, for the same reason the P-Index
// methodology is: a page that states a threshold the engine is not applying is worse than no page,
// because it is checkable and wrong. Every figure below is read from the same struct the fit uses.
func (h *Handler) methodology(w http.ResponseWriter, r *http.Request) {
	fit, build := DefaultConfig(), DefaultBuildConfig()
	snap := h.svc.Snapshot()

	out := map[string]any{
		"question": "Which model plays these games best, with the developer's harness held constant?",
		"why_not_win_rate": "Every match confounds the model with the agent built around it. A strong " +
			"engineer on a weak model beats a weak engineer on a strong one, so a win-rate table " +
			"cannot tell you which it is showing. Publishing one would look exactly like an answer.",

		"model": map[string]any{
			"name": "Bradley-Terry, fitted as a regularized logistic regression",
			"ties": "Davidson (1970) tie extension. Draws are modelled, not discarded: closely " +
				"matched models draw MOST often, so dropping draws throws away the comparisons " +
				"that carry the most information about near-equal pairs.",
			"harness_control": "Each (developer, scaffold) pair enters as its own ability term, so a " +
				"developer who ran two models on ONE harness identifies the difference between them " +
				"with the harness held constant. This is a paired comparison, not an adjustment " +
				"applied afterwards.",
			"n_player": "Placements are rank-broken into pairwise comparisons, consistent for the " +
				"Plackett-Luce family under full rank-breaking (Azari Soufiani et al. 2014). Each " +
				"MATCH contributes total weight 1 however many seats it had, so a six-player table " +
				"does not outweigh a two-player one by fifteen times.",
			"regularization": map[string]any{
				"model_l2":   fit.L2,
				"stratum_l2": fit.L2Stratum,
				"why": "A ridge penalty, equivalently a zero-mean Normal prior. It makes the fit " +
					"strictly convex so the optimum is unique even for a model that never lost, and " +
					"shrinks thinly-observed models toward the mean instead of letting a 2-0 record " +
					"produce an unbounded rating.",
			},
		},

		"uncertainty": map[string]any{
			"bootstrap_replicates": fit.BootstrapReplicates,
			"clustering": "Resampled by MATCH, not by comparison. An N-player match yields several " +
				"correlated comparisons; resampling them independently would treat one match as " +
				"several and narrow every interval by roughly the square root of the seats per match.",
			"ranking": "Rows are ordered by the LOWER bound of the interval, not the point estimate. " +
				"Ranking on the point estimate systematically promotes the least-observed models.",
			"rank_stability": "The share of bootstrap replicates in which a model held its rank. A " +
				"rank held in 60% of replicates and one held in 99% are different claims, and " +
				"printing both as an integer position hides the difference.",
		},

		"eligibility": map[string]any{
			"verified_only": "Only decisions PROVEN LLM-backed by a per-turn proof count. A model " +
				"board built on self-reported attribution ranks claims, not models.",
			"min_coverage":    build.MinCoverage,
			"min_comparisons": fit.MinComparisons,
			"provisional": "Models below the comparison threshold are SHOWN and marked provisional. " +
				"Hiding them would make the board look complete when it is not.",
			"separability": "The share of a model's evidence that came from a harness which also ran " +
				"another model. A model run by exactly one developer on one scaffold is not " +
				"distinguishable from that developer, however many matches it played — a high rating " +
				"with low separability is a statement about a person, not a model.",
		},

		"excluded_games": map[string]any{
			"mafia": "Excluded structurally, not for want of effort. Mafia is a team game: within one " +
				"match, seats holding the same role always share an outcome (a tie, carrying no " +
				"information), and seats holding different roles differ only because of which team " +
				"won — which random role assignment decides. Conditioning on role removes every " +
				"informative comparison; not conditioning ranks models by the roles they were dealt. " +
				"Mafia's signal is a per-seat quantity, accuracy above the chance rate for the role " +
				"held, and it belongs on the board that reports that.",
		},

		"manipulation": map[string]any{
			"the_known_attack": "Arena leaderboards are distorted less by noise than by SELECTION: a " +
				"provider privately tests N variants and submits the best, violating Bradley-Terry's " +
				"assumption that comparisons are sampled independently of their outcome. Singh et " +
				"al. (2025) measure roughly +100 Elo from ten private variants.",
			"why_pyyol_refuses_it": "Every comparison here is a proof-bound decision recorded when it " +
				"happened, with coins moved against it. There is no submission step to select at, " +
				"and the losses are already in the ledger.",
			"what_we_cannot_refuse": "A developer abandoning a losing model. That is why each row " +
				"carries its full record and its coverage rather than a rating alone.",
		},

		"references": []string{
			"Bradley & Terry (1952), Rank analysis of incomplete block designs",
			"Davidson (1970), On extending the Bradley-Terry model to accommodate ties",
			"Azari Soufiani et al. (2014), Generalized method-of-moments for rank aggregation",
			"Chiang et al. (2024), Chatbot Arena, arXiv:2403.04132",
			"Singh et al. (2025), The Leaderboard Illusion, arXiv:2504.20879",
		},
	}
	if snap != nil {
		// The state the method was last applied to, so the description and the data a reader is
		// looking at cannot be from different worlds.
		out["current"] = map[string]any{
			"computed_at": snap.ComputedAt,
			"window_days": snap.WindowDays,
			"models":      len(snap.Board.Ratings),
			"comparisons": snap.Board.Comparisons,
			"matches":     snap.Board.Matches,
			"harnesses":   snap.Board.Strata,
			"converged":   snap.Board.Converged,
			// Published because it is the honest answer to "why is my model missing".
			"seats_excluded": snap.Board.SeatsExcluded,
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}
