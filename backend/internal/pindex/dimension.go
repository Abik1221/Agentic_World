// Package pindex computes the P-Index (Pyyol Index) — a developer's composite
// reputation score. It is a MODULAR scoring engine: the P-Index is a weighted blend
// of independently-configurable Dimensions, each mapping a snapshot of a developer's
// inputs to a 0–1000 sub-score. New dimensions (reasoning, planning, memory, …) plug
// in by implementing Dimension and adding a weight to the config — with no change to
// the existing dimensions or the stored ratings.
//
// The engine is PURE and DETERMINISTIC: Compute is a function of (inputs, config)
// only — no DB, no wall-clock (the "as of" time is part of the inputs) — so any
// historical P-Index is reproducible from its recorded config version + inputs.
package pindex

import "time"

// ArenaInput is a developer's standing in one arena for the season: the rating of
// their best agent there, its uncertainty, and how many matches back it.
type ArenaInput struct {
	Game    string
	Rating  int     // displayed rating of the developer's best agent in this arena
	RD      float64 // Glicko rating deviation (uncertainty), if Algo == glicko2
	Sigma   float64 // TrueSkill sigma (uncertainty), if Algo == trueskill
	Algo    string
	Matches int
}

// DeveloperInputs is the full, pre-assembled snapshot the engine scores. It is
// gathered once (by the store) and shared by every dimension.
type DeveloperInputs struct {
	UserPublicID string
	Season       int

	Arenas []ArenaInput // per-arena standings (best agent per arena)

	TotalMatches   int // rated matches this season (across the developer's agents)
	DistinctArenas int

	AvgOppRating      float64 // mean opponent rating faced (rating_before at match time)
	AvgOppRatingOnWin float64 // mean opponent rating in the developer's WINS

	// Engine-measured intelligence signals, season-aggregated across the
	// developer's agents (from the benchmark rollup). These are platform-measured,
	// not self-reported, so they can't be gamed — the safe backbone for reputation.
	BenchDecisions int     // benchmarked decisions this season (sample size / gate)
	LegalRate      float64 // 0–1: share of moves that were legal (not force-defaulted)
	FallbackRate   float64 // 0–1: share of moves that fell back (timeout/illegal/transport)
	AvgLatencyMS   float64 // mean decision latency across benchmarked moves

	LastMatchAt time.Time // most recent rated match
	AsOf        time.Time // recompute reference time (activity recency is relative to this)
}

// SubScore is one dimension's output: a 0–1000 score plus a human-readable reason
// the transparency surface renders ("why it changed / what to improve").
type SubScore struct {
	Key    string  `json:"key"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

// Dimension maps developer inputs to a 0–1000 sub-score. Implementations MUST be
// pure and deterministic.
type Dimension interface {
	Key() string
	Score(in DeveloperInputs, cfg Config) SubScore
}

// Contribution is one dimension's weighted share of the final P-Index.
type Contribution struct {
	Key      string  `json:"key"`
	Score    float64 `json:"score"`    // the dimension's 0–1000 sub-score
	Weight   float64 `json:"weight"`   // its configured weight
	Weighted float64 `json:"weighted"` // score × weight (its points in the P-Index)
	Reason   string  `json:"reason"`
}

// Result is a computed P-Index with its full, auditable breakdown.
type Result struct {
	PIndex        float64        `json:"p_index"`
	ConfigVersion int            `json:"config_version"`
	Contributions []Contribution `json:"contributions"`
}

// Sub returns the sub-score for a dimension key (0 if absent) — convenience for the
// store when persisting the per-dimension columns.
func (r Result) Sub(key string) float64 {
	for _, c := range r.Contributions {
		if c.Key == key {
			return c.Score
		}
	}
	return 0
}
