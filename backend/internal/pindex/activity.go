package pindex

import "fmt"

const keyActivity = "activity"

// Activity (15%) — rewards active, varied, recent participation WITHOUT rewarding
// spam. Three parts: volume with diminishing returns (m/(m+K) saturates, so the
// 1000th match adds almost nothing), arena diversity (playing multiple arenas), and
// recency (are they active now). Flagged matches are excluded upstream, so farming
// can't inflate volume.
type Activity struct{}

func (Activity) Key() string { return keyActivity }

func (Activity) Score(in DeveloperInputs, cfg Config) SubScore {
	// Volume: diminishing returns.
	volume := 0.0
	if k := cfg.Activity.MatchK; in.TotalMatches > 0 {
		volume = float64(in.TotalMatches) / (float64(in.TotalMatches) + k)
	}
	// Diversity: fraction of the target number of arenas.
	diversity := 0.0
	if cfg.Activity.DiversityTarget > 0 {
		diversity = clamp(float64(in.DistinctArenas)/cfg.Activity.DiversityTarget, 0, 1)
	}
	// Recency: full credit inside the active window, decaying to 0 over 2× the window.
	recency := 0.0
	if !in.LastMatchAt.IsZero() && cfg.Activity.RecencyDays > 0 {
		days := in.AsOf.Sub(in.LastMatchAt).Hours() / 24
		switch {
		case days <= cfg.Activity.RecencyDays:
			recency = 1
		case days >= 2*cfg.Activity.RecencyDays:
			recency = 0
		default:
			recency = 1 - (days-cfg.Activity.RecencyDays)/cfg.Activity.RecencyDays
		}
	}
	score := (0.6*volume + 0.25*diversity + 0.15*recency) * cfg.Scale
	return SubScore{
		Key:    keyActivity,
		Score:  clamp(score, 0, cfg.Scale),
		Reason: fmt.Sprintf("%d matches, %d arenas, recency %.0f%%", in.TotalMatches, in.DistinctArenas, recency*100),
	}
}
