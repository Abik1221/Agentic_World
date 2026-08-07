package modelboard

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// HTTP surface for the model board.
//
// Two endpoints, and the second is not optional. A ranking of commercial models by strategic play
// is a claim strong enough that publishing the number without the method would be irresponsible —
// so the methodology is served from the SAME constants the estimator runs on, and cannot describe
// a threshold the fit is not using.
type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/benchmark/modelboard", h.board)
	r.Get("/v1/benchmark/modelboard/methodology", h.methodology)
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
