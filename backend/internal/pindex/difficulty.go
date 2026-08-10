package pindex

import "fmt"

const keyDifficulty = "difficulty"

// Difficulty (15%) — the quality of opponents faced. It normalizes a blend of the
// mean opponent rating over ALL the developer's matches and the mean opponent rating
// in their WINS, so beating strong agents counts for more than merely facing them.
// Opponent ratings are read from the per-match snapshot (rating_before), i.e. the
// opponent's strength AT THE TIME of the match.
type Difficulty struct{}

func (Difficulty) Key() string { return keyDifficulty }

func (Difficulty) Score(in DeveloperInputs, cfg Config) SubScore {
	if in.AvgOppRating <= 0 {
		return SubScore{Key: keyDifficulty, Score: 0, Reason: "no rated opponents faced yet"}
	}
	blended := in.AvgOppRating
	if in.AvgOppRatingOnWin > 0 {
		// Weight wins-vs-strong: half the mean opponent faced, half the mean beaten.
		blended = 0.5*in.AvgOppRating + 0.5*in.AvgOppRatingOnWin
	}
	score := normalize(blended, cfg.Norm.Low, cfg.Norm.High, cfg.Scale)
	return SubScore{
		Key:    keyDifficulty,
		Score:  score,
		Reason: fmt.Sprintf("avg opponent rating %.0f (beaten %.0f)", in.AvgOppRating, in.AvgOppRatingOnWin),
	}
}

// Explain publishes this dimension's method.
func (Difficulty) Explain(cfg Config) DimensionDoc {
	return DimensionDoc{
		Name:     "Difficulty Faced",
		Measures: "The strength of the opposition you actually played, and beat.",
		Rationale: "This is what makes Arena Standing meaningful rather than farmable. Without " +
			"it, the cheapest route to a high score is repeatedly beating the weakest " +
			"opponent available; with it, that route raises one dimension and suppresses " +
			"another.",
		Formulas: []Formula{{
			Expression: "score = scale × clamp((mean_opponent_rating − low) / (high − low), 0, 1)",
			Where: map[string]string{
				"mean_opponent_rating": "opponents' ratings AS THEY STOOD at match time, not today — crediting you for an opponent who improved later would reward waiting rather than winning",
			},
		}},
		Parameters: map[string]any{"norm_low": cfg.Norm.Low, "norm_high": cfg.Norm.High},
		Gameable: "Deliberately seeking strong opponents raises this dimension — and that is " +
			"the intended incentive, not an exploit.",
	}
}
