package pindex

import "fmt"

const keyArena = "arena"

// Arena (50%) — the primary dimension. It is the matches-weighted mean of the
// developer's per-arena ratings, each normalized to 0–1000. Playing more in an arena
// gives it more weight (saturating at match_cap); an arena with fewer than
// min_matches games counts only partially (provisional), so a single lucky arena
// can't dominate. Uses the developer's BEST agent per arena (assembled upstream).
type Arena struct{}

func (Arena) Key() string { return keyArena }

func (Arena) Score(in DeveloperInputs, cfg Config) SubScore {
	if len(in.Arenas) == 0 {
		return SubScore{Key: keyArena, Score: 0, Reason: "no rated arenas yet — play a ranked match to start your Arena rating"}
	}
	var weightedSum, weightTotal float64
	best := 0
	bestGame := ""
	for _, a := range in.Arenas {
		norm := normalize(float64(a.Rating), cfg.Norm.Low, cfg.Norm.High, cfg.Scale)
		w := float64(a.Matches)
		if cap := float64(cfg.Arena.MatchCap); cap > 0 && w > cap {
			w = cap
		}
		if a.Matches < cfg.Arena.MinMatches && cfg.Arena.MinMatches > 0 {
			// Provisional: scale the weight down proportionally to the sample size.
			w *= float64(a.Matches) / float64(cfg.Arena.MinMatches)
		}
		weightedSum += norm * w
		weightTotal += w
		if a.Rating > best {
			best, bestGame = a.Rating, a.Game
		}
	}
	score := 0.0
	if weightTotal > 0 {
		score = weightedSum / weightTotal
	}
	return SubScore{
		Key:    keyArena,
		Score:  clamp(score, 0, cfg.Scale),
		Reason: fmt.Sprintf("across %d arena(s); strongest is %s at %d", len(in.Arenas), bestGame, best),
	}
}
