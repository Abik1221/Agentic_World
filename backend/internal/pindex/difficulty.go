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
