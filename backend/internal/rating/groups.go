package rating

import "sort"

// Group comparison: the same season's facts rolled up by provider, by vendor, by
// openness, by hosting and by family.
//
// This exists because the per-model board answers "which model won" but not the
// question people actually argue about — "are open-weight models competitive yet",
// "is OpenAI or Anthropic better at social deduction", "is self-hosting worth it".
// Those need the models POOLED, and pooling has to happen over the raw counters:
// averaging the per-model rates would weight a model with four games the same as one
// with four hundred.
//
// Every group is built by walking the model rows, so a model that has never been seen
// before joins its groups on its first finished match with no configuration anywhere.

// GroupStat is one comparison group's pooled performance. The metric names match
// ModelStat deliberately — the UI renders both through the same formatters, and a
// figure that meant something different here would be a trap.
type GroupStat struct {
	// Kind is the axis (GroupProvider, GroupVendor, GroupOpenness, GroupHosting,
	// GroupFamily); Key is the value within it.
	Kind string `json:"kind"`
	Key  string `json:"key"`

	// Models is how many distinct models pooled into this row, and Agents how many
	// distinct agents. Both are published so a "group" of one is obvious at a glance
	// rather than looking like a trend.
	Models int `json:"models"`
	Agents int `json:"agents"`

	Matches int `json:"matches"`
	Games   int `json:"games"`
	Wins    int `json:"wins"`
	Losses  int `json:"losses"`
	Ties    int `json:"ties"`

	WinRate     float64 `json:"win_rate"`
	WinRateCI   float64 `json:"win_rate_ci"`
	Preliminary bool    `json:"preliminary"`
	AvgElo      int     `json:"avg_elo"`
	CoinsWon    int64   `json:"coins_won"`

	Decisions         int64   `json:"decisions"`
	LegalRate         float64 `json:"legal_rate"`
	FallbackRate      float64 `json:"fallback_rate"`
	AvgLatencyMs      int     `json:"avg_latency_ms"`
	AvgMatchSeconds   float64 `json:"avg_match_seconds"`
	DecisionsPerMatch float64 `json:"decisions_per_match"`

	Tokens            int64   `json:"tokens"`
	TokensPerMatch    float64 `json:"tokens_per_match"`
	TokensPerDecision float64 `json:"tokens_per_decision"`
	TokensPerWin      float64 `json:"tokens_per_win"`

	EstCostUSD      float64 `json:"est_cost_usd"`
	VerifiedCostUSD float64 `json:"verified_cost_usd"`
	CostPerMatch    float64 `json:"cost_per_match"`
	CostPerWin      float64 `json:"cost_per_win"`
	CostBasis       string  `json:"cost_basis,omitempty"`
	// Verified is the group's pooled coverage: bound decisions over logged decisions across
	// every model in it. Pooled from raw COUNTS, not averaged from per-model fractions — a
	// group is verified to the extent its PLAY was proven, and averaging would let one tiny
	// fully-covered model carry a large uncovered one over the threshold.
	Verified CoverageStat `json:"verified"`

	Intelligence int `json:"intelligence"`

	// Top names the group pooled, best-ranked first, so a reader can see WHAT is
	// behind "open-weights: 54% win rate" without expanding anything. Capped.
	TopModels []string `json:"top_models,omitempty"`
}

// maxTopModels bounds the sample of member names shown on a group row. Enough to
// recognise the group, short enough not to become a second leaderboard.
const maxTopModels = 5

// accumulator sums raw counters for one group before any rate is derived.
type accumulator struct {
	GroupStat
	models     map[string]bool
	agentTotal int
	// Weighted inputs for the averages, kept separate from the published figures
	// because each has its own correct denominator.
	eloWeighted  float64
	eloWeight    int
	latencySumMS float64
	playSeconds  float64
	timedMatches int
	legal        int64
	fallbacks    int64
	// Pooled coverage counts, divided once in finish() for the same reason as every other
	// average here: each figure has its own correct denominator.
	boundDecisions  int
	loggedDecisions int
	names           []string
}

// BuildGroups rolls the model rows up along every comparison axis.
//
// Rows the caller filtered out never reach here, so a group total always equals the
// sum of the models actually shown — a group that counted hidden rows would not
// reconcile against the board above it.
func BuildGroups(models []ModelStat) []GroupStat {
	byKind := map[string]map[string]*accumulator{
		GroupProvider: {},
		GroupVendor:   {},
		GroupOpenness: {},
		GroupHosting:  {},
		GroupFamily:   {},
	}

	for i := range models {
		m := &models[i]
		c := Classify(m.Provider, m.Model)
		for kind, key := range map[string]string{
			GroupProvider: c.Provider,
			GroupVendor:   c.Vendor,
			GroupOpenness: c.Openness,
			GroupHosting:  c.Hosting,
			GroupFamily:   c.Family,
		} {
			if key == "" {
				key = "unknown"
			}
			bucket := byKind[kind]
			acc := bucket[key]
			if acc == nil {
				acc = &accumulator{models: map[string]bool{}}
				acc.Kind, acc.Key = kind, key
				bucket[key] = acc
			}
			acc.add(m)
		}
	}

	var out []GroupStat
	for _, bucket := range byKind {
		for _, acc := range bucket {
			out = append(out, acc.finish())
		}
	}
	// Deterministic order: axis, then strongest group first. Without a stable sort the
	// same data would render in a different order on every request.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Intelligence != out[j].Intelligence {
			return out[i].Intelligence > out[j].Intelligence
		}
		if out[i].WinRate != out[j].WinRate {
			return out[i].WinRate > out[j].WinRate
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func (a *accumulator) add(m *ModelStat) {
	if !a.models[m.Provider+"/"+m.Model] {
		a.models[m.Provider+"/"+m.Model] = true
		a.Models++
		a.names = append(a.names, m.Model)
	}
	// Agents is a SUM, not a distinct count: the per-model rows already counted each
	// agent once within their own arena, and this layer cannot see agent ids to
	// deduplicate across them. Documented rather than silently approximated — it reads
	// as "agent-model pairings", which is the honest reading of what it is.
	a.agentTotal += m.Agents

	a.Matches += m.Matches
	a.Games += m.Games
	a.Wins += m.Wins
	a.Losses += m.Losses
	a.Ties += m.Ties
	a.CoinsWon += m.CoinsWon
	a.Decisions += m.Decisions
	a.legal += m.Legal
	a.fallbacks += m.Fallbacks
	a.Tokens += m.Tokens
	a.EstCostUSD += m.EstCostUSD
	a.VerifiedCostUSD += m.VerifiedCostUSD
	a.boundDecisions += m.Verified.BoundDecisions
	a.loggedDecisions += m.Verified.Decisions

	// ELO is averaged weighted by GAMES, not per model: a model with one rated game
	// must not move a group's rating as much as one with two hundred.
	if m.AvgElo > 0 && m.Games > 0 {
		a.eloWeighted += float64(m.AvgElo) * float64(m.Games)
		a.eloWeight += m.Games
	}
	// Latency is re-derived from the total decision time the model spent, recovered
	// from its mean — pooling means-of-means would weight a rarely-used model equally.
	a.latencySumMS += float64(m.AvgLatencyMs) * float64(m.Decisions)
	a.playSeconds += m.PlaySeconds
	a.timedMatches += m.TimedMatches
}

func (a *accumulator) finish() GroupStat {
	g := a.GroupStat
	g.Agents = a.agentTotal

	if decisive := g.Wins + g.Losses; decisive > 0 {
		g.WinRate = float64(g.Wins) / float64(decisive)
		g.WinRateCI = wilsonHalfWidth95(g.Wins, decisive)
	}
	g.Preliminary = g.Games < prelimMinGames

	if a.eloWeight > 0 {
		g.AvgElo = int(a.eloWeighted/float64(a.eloWeight) + 0.5)
	}
	if g.Decisions > 0 {
		g.LegalRate = float64(a.legal) / float64(g.Decisions)
		g.FallbackRate = float64(a.fallbacks) / float64(g.Decisions)
		g.AvgLatencyMs = int(a.latencySumMS/float64(g.Decisions) + 0.5)
		g.TokensPerDecision = float64(g.Tokens) / float64(g.Decisions)
	}
	if a.timedMatches > 0 {
		g.AvgMatchSeconds = a.playSeconds / float64(a.timedMatches)
	}
	if g.Matches > 0 {
		g.TokensPerMatch = float64(g.Tokens) / float64(g.Matches)
		g.DecisionsPerMatch = float64(g.Decisions) / float64(g.Matches)
	}
	if g.Wins > 0 {
		g.TokensPerWin = float64(g.Tokens) / float64(g.Wins)
	}

	// Coverage first: the cost rule below reads it, and a zero-value CoverageStat would
	// silently make every group fall back to self-reported cost.
	g.Verified = NewCoverage(a.loggedDecisions, a.boundDecisions)

	// Same rule as a model row, through the SAME function — a cost basis that means one thing
	// on a model row and another on the group containing it would be worse than showing
	// neither. Verified spend only when coverage says it represents the group.
	cost, basis := CostForRanking(g.VerifiedCostUSD, g.EstCostUSD, g.Verified)
	g.CostBasis = basis
	if g.Matches > 0 {
		g.CostPerMatch = cost / float64(g.Matches)
	}
	if g.Wins > 0 {
		g.CostPerWin = cost / float64(g.Wins)
	}

	g.Intelligence = intelligenceScore(g.Decisions, g.LegalRate, g.FallbackRate, g.AvgLatencyMs)

	names := append([]string(nil), a.names...)
	sort.Strings(names)
	if len(names) > maxTopModels {
		names = names[:maxTopModels]
	}
	g.TopModels = names
	return g
}
