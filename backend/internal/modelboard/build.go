package modelboard

import (
	"fmt"
	"sort"
)

// Seat is one agent's participation in one match, as the store reads it.
//
// The unit is a SEAT rather than a match because the same match contributes several rows and
// each carries its own model, harness and outcome. Pairing happens here, in code, rather than in
// SQL: the rules about which seats may be compared are the substance of this package and belong
// somewhere they can be read and tested.
type Seat struct {
	MatchID string
	Game    string
	AgentID string
	// Model is the VERIFIED model — read off what the gateway saw the provider return on a bound
	// call. Empty when this seat's play was never proven, in which case the seat cannot enter the
	// board at all: a model board built on self-reported attribution ranks claims, not models.
	Model string
	// Scaffold is the harness fingerprint. Combined with DeveloperID it forms the stratum this
	// seat's ability is attributed to.
	Scaffold    string
	DeveloperID string
	// Result is "win", "loss" or "draw".
	Result string
	// Coverage is the share of this seat's decisions that were proven LLM-backed. Seats below
	// the threshold are excluded — a seat we only half observed is a seat whose model we only
	// half know.
	Coverage float64
	// CoverageKnown distinguishes "no decisions logged" from "nothing proven".
	CoverageKnown bool
	// Role is the game role where one exists (Mafia). Carried so the exclusion below can be
	// justified from the data rather than asserted.
	Role string
}

// Stratum is the harness identity this seat's ability is attributed to.
//
// Developer AND scaffold, not either alone. Developer alone would pool a person's every harness
// version into one ability, so improving a prompt would look like the model getting better.
// Scaffold alone would pool two developers who happen to have written the same system prompt,
// which says nothing about either of them.
func (s Seat) Stratum() string { return s.DeveloperID + "/" + s.Scaffold }

// BuildConfig gates which seats may enter the board.
type BuildConfig struct {
	// MinCoverage is the proven share a seat needs. Defaults to the verified-tier threshold: the
	// board's whole claim is that it ranks models rather than assertions, and a seat whose model
	// attribution covers a fifth of its decisions does not support that claim.
	MinCoverage float64
	// Games restricts which arenas contribute. Empty means every eligible arena.
	Games []string
}

// DefaultBuildConfig matches the verified tier's coverage requirement.
func DefaultBuildConfig() BuildConfig {
	return BuildConfig{MinCoverage: 0.90}
}

// pairwiseGames are the arenas whose outcomes support a within-match pairwise comparison.
//
// Goofspiel is zero-sum and two-player: one comparison per match, draws included.
//
// Monopoly is N-player, and its result is a PARTITION (winners, losers, draws) rather than a full
// placement. That still yields comparisons — every winner beat every loser — which is a valid
// rank-breaking of a tiered ordering.
//
// MAFIA IS DELIBERATELY ABSENT, and the reason is structural rather than unfinished work.
//
// Mafia is a team game: the whole team wins or loses together. So within one match, two seats
// holding the SAME role always share an outcome, which is a tie and carries no information about
// either model. And two seats holding DIFFERENT roles differ in outcome only because of which
// team won — which role assignment decides, and role assignment is random. Conditioning on role
// therefore removes every informative Mafia comparison, and NOT conditioning on it means ranking
// models by the roles they were dealt.
//
// There is no way to fix that by weighting: a within-match pairwise model is the wrong estimator
// for a team game with hidden roles. Mafia's signal is a per-seat quantity — accuracy above the
// chance rate for the role held — which is what internal/skill computes, and it belongs on the
// board that reports it rather than smuggled into this one.
var pairwiseGames = map[string]bool{
	"goofspiel": true,
}

// PairwiseGames is the set above, sorted, for a caller that needs to narrow a query to it.
//
// Exported so the seat query can be BUILT from this map instead of restating its contents in
// SQL. That matters because the query now filters on it: 600,895 seats were being read out of
// Postgres and handed to BuildComparisons, which discarded 593,530 of them on this one
// condition — a team game has no pairwise comparison to contribute. Filtering in the database
// removes 98.8% of the rows before they are ever materialised.
//
// A second list in SQL would have been the obvious way to do that and the wrong one: the two
// would agree until someone added a game, and then the board would either silently ignore it
// or silently include it depending on which list they found. There is one list, and the SQL is
// generated from it.
func PairwiseGames() []string {
	out := make([]string, 0, len(pairwiseGames))
	for g := range pairwiseGames {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// resultRank orders outcomes so a comparison's direction is decidable. Higher beats lower.
func resultRank(result string) (int, bool) {
	switch result {
	case "win":
		return 2, true
	case "draw":
		return 1, true
	case "loss":
		return 0, true
	default:
		// Includes the empty result an unfinished or aborted match leaves behind. Not a draw:
		// "we do not know how this ended" and "it ended level" are different, and treating the
		// first as the second would feed the board outcomes that never happened.
		return 0, false
	}
}

// BuildComparisons turns seats into the pairwise comparisons the estimator consumes.
//
// Returns the comparisons and a census of what was dropped and why. The census is not
// diagnostics-for-developers — it is part of the published methodology, because a model missing
// from a board is a claim about that model and a reader deserves to know whether it was never
// played, never verified, or excluded by a rule.
func BuildComparisons(seats []Seat, cfg BuildConfig) ([]Comparison, map[string]int) {
	return buildComparisons(seats, cfg, nil)
}

// buildComparisons is BuildComparisons plus exclusions the caller already counted.
//
// `preCounted` carries reasons the SEAT QUERY resolved before returning rows — today only the
// non-pairwise games it filters out. They are merged into the same published map, so
// seats_excluded still accounts for every seat in the window regardless of which layer dropped
// it. Without this the board would report "0 seats excluded for a non-pairwise game" while
// having excluded half a million, which is worse than not publishing the number at all.
func buildComparisons(seats []Seat, cfg BuildConfig, preCounted map[string]int) ([]Comparison, map[string]int) {
	excluded := map[string]int{}
	for reason, n := range preCounted {
		excluded[reason] += n
	}
	allowGame := func(g string) bool {
		if !pairwiseGames[g] {
			return false
		}
		if len(cfg.Games) == 0 {
			return true
		}
		for _, want := range cfg.Games {
			if want == g {
				return true
			}
		}
		return false
	}

	byMatch := map[string][]Seat{}
	for _, s := range seats {
		switch {
		case !pairwiseGames[s.Game]:
			// Counted under a name that says WHY, so "where is Mafia" has an answer on the page.
			excluded["game_not_pairwise_"+s.Game]++
			continue
		case !allowGame(s.Game):
			excluded["game_filtered"]++
			continue
		case s.Model == "":
			// The single most important exclusion: no verified model means nothing to rank.
			excluded["no_verified_model"]++
			continue
		case !s.CoverageKnown:
			excluded["coverage_unknown"]++
			continue
		case s.Coverage < cfg.MinCoverage:
			excluded["coverage_below_threshold"]++
			continue
		case s.Scaffold == "":
			// Without a harness fingerprint the seat cannot be assigned to a stratum, so its
			// evidence could not be separated from the developer behind it. Admitting it under an
			// empty stratum would silently pool every unfingerprinted agent into one "harness".
			excluded["no_scaffold"]++
			continue
		}
		if _, ok := resultRank(s.Result); !ok {
			excluded["no_result"]++
			continue
		}
		byMatch[s.MatchID] = append(byMatch[s.MatchID], s)
	}

	// Sorted so the comparison list — and therefore the floating-point summation order in the
	// likelihood — depends only on the data.
	matchIDs := make([]string, 0, len(byMatch))
	for id := range byMatch {
		matchIDs = append(matchIDs, id)
	}
	sort.Strings(matchIDs)

	var out []Comparison
	for _, id := range matchIDs {
		group := byMatch[id]
		if len(group) < 2 {
			// One eligible seat means the opponent was excluded, so there is nobody to compare
			// against. Counted, because a match that contributes nothing is worth seeing in the
			// census — it is usually a sign that one side is unverified.
			excluded["match_had_one_eligible_seat"]++
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].AgentID < group[j].AgentID })

		// Every ordered pair once. Same-model pairs are left in and dropped by Estimate, which
		// counts them: the reason ("two agents ran the same model") is worth reporting separately
		// from the reasons here.
		var pairs []Comparison
		for i := 0; i < len(group); i++ {
			for j := i + 1; j < len(group); j++ {
				a, b := group[i], group[j]
				ra, _ := resultRank(a.Result)
				rb, _ := resultRank(b.Result)
				var o Outcome
				switch {
				case ra > rb:
					o = Win
				case rb > ra:
					o = Loss
				default:
					o = Draw
				}
				pairs = append(pairs, Comparison{
					MatchID: id,
					ModelA:  a.Model, ModelB: b.Model,
					StratumA: a.Stratum(), StratumB: b.Stratum(),
					Outcome: o,
				})
			}
		}
		// Each MATCH contributes total weight 1, however many seats it had.
		//
		// Unit weights would let a six-player Monopoly table carry fifteen times the weight of a
		// Goofspiel match in the likelihood, so the board would quietly become a Monopoly board
		// with a Goofspiel footnote. Normalizing makes the cross-game weighting an explicit
		// decision — one match, one unit of evidence — rather than an artefact of table size.
		//
		// This concerns the POINT ESTIMATE only. The correlation between comparisons from one
		// table is handled separately, by clustering the bootstrap on the match.
		w := 1.0 / float64(len(pairs))
		for k := range pairs {
			pairs[k].Weight = w
		}
		out = append(out, pairs...)
	}
	return out, excluded
}

// Board is a fitted board plus the census of what never reached the fit.
type Board struct {
	Fit
	// SeatsExcluded is why seats were dropped before fitting, keyed by reason.
	SeatsExcluded map[string]int `json:"seats_excluded,omitempty"`
	// MinCoverage is the coverage gate that was applied, published so a reader knows which
	// population the board describes.
	MinCoverage float64 `json:"min_coverage"`
}

// Build assembles and fits a board from seats in one call.
func Build(seats []Seat, bc BuildConfig, fc Config) Board {
	return BuildWithExclusions(seats, nil, bc, fc)
}

// BuildWithExclusions is Build for a seat source that pre-filtered, and so already knows why
// some seats are absent. See buildComparisons.
func BuildWithExclusions(seats []Seat, preCounted map[string]int, bc BuildConfig, fc Config) Board {
	cmp, excluded := buildComparisons(seats, bc, preCounted)
	b := Board{Fit: Estimate(cmp, fc), SeatsExcluded: excluded, MinCoverage: bc.MinCoverage}
	return b
}

// Summary is a one-line description of what a board rests on, for logs and the methodology page.
func (b Board) Summary() string {
	return fmt.Sprintf("%d models from %d comparisons across %d matches and %d harnesses "+
		"(coverage >= %.0f%%, nu=%.3f, converged=%v)",
		len(b.Ratings), b.Comparisons, b.Matches, b.Strata, b.MinCoverage*100, b.Nu, b.Converged)
}
