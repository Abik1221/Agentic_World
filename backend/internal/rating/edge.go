package rating

import "sort"

// Skill above model — the metric that makes this an AGENTIC benchmark rather than a
// second model leaderboard.
//
// THE PROBLEM WITH RANKING DEVELOPERS BY WIN RATE. A developer running Claude Opus and
// one running an 8B model on their own GPU are not playing the same game. Rank them
// together on raw win rate and the board measures budget, not engineering: the way to
// climb is to buy a better model, and a developer who squeezes a remarkable result out
// of a small model is invisible below people who did less with more.
//
// WHAT THIS MEASURES INSTEAD. Every model has a population win rate — what agents
// running it achieve on average, across everyone. A developer's EDGE is how far above
// (or below) that baseline they land with the models they actually ran, weighted by how
// many games they played on each:
//
//	expected = Σ (games on model m × population win rate of m) / Σ games
//	edge     = actual win rate − expected
//
// A positive edge means: given the same model, this developer wins more than the model
// alone accounts for. That is prompt design, state representation, search, fallback
// handling, timing — the engineering. It is the only figure on the platform that is
// comparable between someone on a frontier API and someone on a laptop.
//
// The baseline is the SAME per-model win rate the public board publishes, so a reader
// can check any edge by hand from two numbers already on the site.
//
// HONESTY CONSTRAINTS, in order of how badly getting them wrong would mislead:
//
//   - The baseline INCLUDES the developer being scored. With few players a developer
//     with most of a model's games is largely their own baseline, so their edge is
//     pulled toward zero. That is the conservative direction — it understates a strong
//     developer rather than inventing an edge — and Contribution reports how much of
//     the baseline is their own play, so the reader can see when it applies.
//   - Edge carries a confidence interval and a preliminary tag, like every other rate
//     on the platform. A +12pp edge over nine games is noise wearing a number.
//   - A model with no population baseline yet (only this developer has run it)
//     contributes to the games total but NOT to expected — inventing a baseline from a
//     single participant would make the edge a statement about nothing.

// DeveloperEdge is one developer's performance relative to the models they ran.
type DeveloperEdge struct {
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`

	Agents  int `json:"agents"`
	Matches int `json:"matches"`
	Games   int `json:"games"`
	Wins    int `json:"wins"`
	Losses  int `json:"losses"`
	Ties    int `json:"ties"`

	WinRate   float64 `json:"win_rate"`
	WinRateCI float64 `json:"win_rate_ci"`
	// ExpectedWinRate is what the models this developer ran achieve on average, weighted
	// by how many games they played on each.
	ExpectedWinRate float64 `json:"expected_win_rate"`
	// Edge is WinRate − ExpectedWinRate, in rate units (0.08 = eight points above the
	// models' own average). The headline figure.
	Edge float64 `json:"edge"`
	// Contribution is the share of the baseline's games that are this developer's own
	// (0..1). High values mean they are largely being compared against themselves.
	Contribution float64 `json:"contribution"`
	Preliminary  bool    `json:"preliminary"`

	// Economics, so "wins more" and "wins cheaply" are separable.
	Tokens       int64   `json:"tokens"`
	TokensPerWin float64 `json:"tokens_per_win"`
	CostUSD      float64 `json:"cost_usd"`
	CostPerWin   float64 `json:"cost_per_win"`
	// CostBasis names which total CostUSD came from (CostVerified or CostSelfReported), or is
	// empty when no usable figure exists. Published for the same reason the model board
	// publishes it: "$0.02 per win" means two different things depending on whether that is
	// the whole bill or the fifth of it that happened to be routed.
	CostBasis string `json:"cost_basis,omitempty"`
	// Verified is the developer's pooled coverage across every model they ran.
	Verified CoverageStat `json:"verified"`

	// Models the developer ran, best-played first. Capped.
	Models []string `json:"models,omitempty"`
	// OpenWeightsOnly marks a developer who never used a proprietary model — the
	// hardest version of the problem, and worth being able to see.
	OpenWeightsOnly bool `json:"open_weights_only"`
}

// DevModelRow is one (developer, model) pairing's season record, as the store reads it.
type DevModelRow struct {
	UserPublicID string
	Username     string
	DisplayName  string
	AvatarURL    string

	Provider string
	Model    string

	Agents  int
	Matches int
	Wins    int
	Losses  int
	Ties    int

	Tokens          int64
	EstCostUSD      float64
	VerifiedCostUSD float64
	// Verified coverage for this (developer, model) pairing, so the pooled cost figure can
	// choose a coherent basis instead of mixing a partial verified slice with complete
	// self-reported totals.
	Verified CoverageStat
}

// maxEdgeModels bounds the model list shown per developer.
const maxEdgeModels = 4

// BuildDeveloperEdges computes every developer's skill-above-model from their per-model
// records and the population baselines on the same board.
//
// Both inputs come from the same season and arena filter, so the baseline a developer is
// scored against is exactly the one the board displays.
func BuildDeveloperEdges(rows []DevModelRow, models []ModelStat, minGames int) []DeveloperEdge {
	if minGames <= 0 {
		minGames = 1
	}

	// Population baseline per model: win rate over DECISIVE games, and the games behind
	// it, so a developer's contribution to their own baseline can be reported.
	type baseline struct {
		winRate  float64
		decisive int
		known    bool
	}
	base := make(map[string]baseline, len(models))
	for _, m := range models {
		key := m.Provider + "/" + m.Model
		if d := m.Wins + m.Losses; d > 0 {
			base[key] = baseline{winRate: m.WinRate, decisive: d, known: true}
		} else {
			base[key] = baseline{}
		}
	}

	type acc struct {
		DeveloperEdge
		// Weighted expectation, accumulated only over models that HAVE a baseline.
		expWeighted float64
		expWeight   int
		ownBaseline int // this developer's decisive games inside the baselines used
		baseTotal   int // the baselines' total decisive games
		byModel     map[string]int
		sawClosed   bool
		sawAny      bool
		// Cost totals kept apart, and coverage counts pooled, so the basis is chosen once on
		// the developer's whole record rather than row by row.
		estCostUSD      float64
		verifiedCostUSD float64
		boundDecisions  int
		loggedDecisions int
	}
	devs := map[string]*acc{}

	for i := range rows {
		r := &rows[i]
		a := devs[r.UserPublicID]
		if a == nil {
			a = &acc{byModel: map[string]int{}}
			a.Username, a.DisplayName, a.AvatarURL = r.Username, r.DisplayName, r.AvatarURL
			devs[r.UserPublicID] = a
		}
		a.Agents += r.Agents
		a.Matches += r.Matches
		a.Wins += r.Wins
		a.Losses += r.Losses
		a.Ties += r.Ties
		a.Tokens += r.Tokens
		// Keep the two totals APART and choose once, in finish(), on the developer's pooled
		// coverage. Choosing per row summed a partial gateway slice from one model with the
		// complete self-reported total from another and divided the mixture by every win —
		// a figure that is not the cost of anything.
		a.estCostUSD += r.EstCostUSD
		a.verifiedCostUSD += r.VerifiedCostUSD
		a.boundDecisions += r.Verified.BoundDecisions
		a.loggedDecisions += r.Verified.Decisions

		key := r.Provider + "/" + r.Model
		a.byModel[key] += r.Wins + r.Losses + r.Ties

		if c := Classify(r.Provider, r.Model); c.Openness == OpenWeights {
			a.sawAny = true
		} else if c.Openness == Proprietary {
			a.sawAny, a.sawClosed = true, true
		}

		// Expectation is weighted by DECISIVE games on that model, matching the unit the
		// baseline is a rate over. A model with no baseline is skipped entirely.
		if d := r.Wins + r.Losses; d > 0 {
			if b, ok := base[key]; ok && b.known {
				a.expWeighted += b.winRate * float64(d)
				a.expWeight += d
				a.ownBaseline += d
				a.baseTotal += b.decisive
			}
		}
	}

	out := make([]DeveloperEdge, 0, len(devs))
	for _, a := range devs {
		e := a.DeveloperEdge
		e.Games = e.Wins + e.Losses + e.Ties
		if e.Games < minGames {
			continue
		}
		if d := e.Wins + e.Losses; d > 0 {
			e.WinRate = float64(e.Wins) / float64(d)
			e.WinRateCI = wilsonHalfWidth95(e.Wins, d)
		}
		if a.expWeight > 0 {
			e.ExpectedWinRate = a.expWeighted / float64(a.expWeight)
			e.Edge = e.WinRate - e.ExpectedWinRate
		}
		if a.baseTotal > 0 {
			e.Contribution = float64(a.ownBaseline) / float64(a.baseTotal)
		}
		e.Preliminary = e.Games < prelimMinGames
		// Coverage first, then the cost basis from it, through the SAME function the model and
		// group rows use. A cost basis that means one thing on the model board and another on
		// the developer board would make the two impossible to reconcile.
		e.Verified = NewCoverage(a.loggedDecisions, a.boundDecisions)
		e.CostUSD, e.CostBasis = CostForRanking(a.verifiedCostUSD, a.estCostUSD, e.Verified)
		if e.Wins > 0 {
			e.TokensPerWin = float64(e.Tokens) / float64(e.Wins)
			// Zero CostUSD means "not measured" here, not "free": CostForRanking returns 0 with
			// an empty basis when the only figure available is known-incomplete, and dividing
			// that by wins would publish 0.00 per win for an agent that spent real money.
			if e.CostUSD > 0 {
				e.CostPerWin = e.CostUSD / float64(e.Wins)
			}
		}
		// Open-weights-only is a positive claim, so it requires having seen at least one
		// classified model — a developer whose models are all unclassified is not
		// silently credited with the hard mode.
		e.OpenWeightsOnly = a.sawAny && !a.sawClosed

		names := make([]string, 0, len(a.byModel))
		for k := range a.byModel {
			names = append(names, k)
		}
		sort.Slice(names, func(i, j int) bool {
			if a.byModel[names[i]] != a.byModel[names[j]] {
				return a.byModel[names[i]] > a.byModel[names[j]]
			}
			return names[i] < names[j]
		})
		for i, n := range names {
			if i >= maxEdgeModels {
				break
			}
			// Show the model, not the provider prefix — the reader is asking "what did
			// they run", not "who served it".
			if idx := indexByte(n, '/'); idx >= 0 && idx+1 < len(n) {
				n = n[idx+1:]
			}
			e.Models = append(e.Models, n)
		}
		out = append(out, e)
	}

	// Highest edge first. Ties break toward the larger sample, because between two equal
	// edges the better-evidenced one is the stronger claim.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Edge != out[j].Edge {
			return out[i].Edge > out[j].Edge
		}
		if out[i].Games != out[j].Games {
			return out[i].Games > out[j].Games
		}
		return out[i].Username < out[j].Username
	})
	return out
}

// indexByte avoids pulling in strings for one lookup in a hot-ish loop.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
