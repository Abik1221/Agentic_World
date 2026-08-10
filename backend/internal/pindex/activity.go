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

// Explain publishes this dimension's method.
func (Activity) Explain(cfg Config) DimensionDoc {
	ac := cfg.Activity
	return DimensionDoc{
		Name:     "Activity & Breadth",
		Measures: "Whether you are currently playing, and across how many arenas.",
		Rationale: "The smallest dimension, and bounded on purpose. A reputation that decays to " +
			"nothing the moment you stop playing punishes people for having other work; one " +
			"that ignores recency entirely lets a long-abandoned agent outrank a live one. " +
			"Diminishing returns on match count are what stop this becoming a grind.",
		Formulas: []Formula{
			{
				Expression: "volume = matches / (matches + k)",
				Where: map[string]string{
					"k": fmt.Sprintf("%.0f — the diminishing-returns constant: match %d adds far less than match 10", ac.MatchK, int(ac.MatchK)*10),
				},
			},
			{
				Expression: "diversity = clamp(distinct_arenas / diversity_target, 0, 1)",
			},
			{
				Expression: "recency = clamp(1 − days_since_last_match / recency_days, 0, 1)",
			},
			{
				Expression: "score = scale × mean(volume, diversity, recency)",
			},
		},
		Parameters: map[string]any{
			"match_k": ac.MatchK, "diversity_target": ac.DiversityTarget, "recency_days": ac.RecencyDays,
		},
		Gameable: "Volume is the one thing here money can buy, which is exactly why it is the " +
			"smallest weight with the steepest diminishing returns.",
	}
}
