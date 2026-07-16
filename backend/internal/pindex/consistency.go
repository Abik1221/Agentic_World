package pindex

import "fmt"

const keyConsistency = "consistency"

// Consistency (20%) — rewards a sustained, settled track record over lucky spikes.
// It is the matches-weighted mean CERTAINTY across arenas (low rating uncertainty =
// high certainty), gated by overall sample size: a developer with fewer than
// min_matches total games can't score full consistency no matter how settled a
// single arena looks. Certainty is normalized per algorithm (Glicko RD or TrueSkill
// sigma) so the two arena types are comparable.
type Consistency struct{}

func (Consistency) Key() string { return keyConsistency }

// certainty maps an arena's uncertainty to [0,1] (1 = fully settled).
func certainty(a ArenaInput, cfg Config) float64 {
	switch a.Algo {
	case "trueskill":
		return 1 - clamp((a.Sigma-cfg.Consistency.SigmaLow)/(cfg.Consistency.SigmaHigh-cfg.Consistency.SigmaLow), 0, 1)
	default: // glicko2
		return 1 - clamp((a.RD-cfg.Consistency.RDLow)/(cfg.Consistency.RDHigh-cfg.Consistency.RDLow), 0, 1)
	}
}

func (Consistency) Score(in DeveloperInputs, cfg Config) SubScore {
	if len(in.Arenas) == 0 || in.TotalMatches == 0 {
		return SubScore{Key: keyConsistency, Score: 0, Reason: "not enough matches to establish consistency"}
	}
	var weightedSum, weightTotal float64
	for _, a := range in.Arenas {
		w := float64(a.Matches)
		weightedSum += certainty(a, cfg) * w
		weightTotal += w
	}
	meanCertainty := 0.0
	if weightTotal > 0 {
		meanCertainty = weightedSum / weightTotal
	}
	// Sample-size gate: full credit only once the developer has min_matches games.
	gate := 1.0
	if cfg.Consistency.MinMatches > 0 {
		gate = clamp(float64(in.TotalMatches)/float64(cfg.Consistency.MinMatches), 0, 1)
	}
	score := meanCertainty * gate * cfg.Scale
	reason := fmt.Sprintf("%.0f%% settled over %d matches", meanCertainty*100, in.TotalMatches)
	if gate < 1 {
		reason = fmt.Sprintf("building a track record (%d/%d matches) — keep playing to raise consistency", in.TotalMatches, cfg.Consistency.MinMatches)
	}
	return SubScore{Key: keyConsistency, Score: clamp(score, 0, cfg.Scale), Reason: reason}
}
