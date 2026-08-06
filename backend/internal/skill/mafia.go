package skill

import (
	"fmt"
	"math"
	"sort"
)

// Mafia decision scoring, against the engine's ground truth.
//
// # Why this needs no judge model
//
// The engine dealt the roles. It knows, with certainty, who the mafia are — so "was that
// a good vote" is not a matter of opinion that an LLM has to adjudicate. It is a lookup.
// Every scorer here compares a decision against a fact the platform already holds.
//
// # Why regret is the wrong shape here, and lift is the right one
//
// In Goofspiel the best action is COMPUTABLE at decision time: both hands are public, so
// an agent that misses the best bid genuinely erred. Mafia is not like that. On day one
// nobody can know who the mafia are; a town agent voting a townsfolk has not blundered,
// it has guessed under uncertainty. Scoring that as maximum regret would mostly measure
// how early the vote happened.
//
// So Mafia is scored as LIFT OVER CHANCE: how much better than random did this agent
// identify the mafia, given how many mafia were alive and how many seats it could pick
// from? Formally, with accuracy p and chance rate c,
//
//	lift = (p - c) / (1 - c)
//
// This is the Cohen's-kappa normalisation. It is 0 for random play, 1 for perfect
// identification, and negative for play that is worse than random — an agent
// systematically voting town is giving up real information and the metric says so.
//
// It also self-corrects for table shape: 3 mafia among 12 is a 27% chance rate, 1 among 4
// is 33%, and raw accuracy would silently rank agents by which tables they happened to
// draw. Lift removes that entirely.
//
// # Role conditioning
//
// Mafia and town are not playing the same game and cannot share a scale. A mafia seat
// that votes a townsfolk is playing WELL; scoring it on town accuracy would punish it for
// doing its job. Each role is scored on its own objective and only then combined.

// MafiaVote is one cast vote, with the ground truth needed to score it.
type MafiaVote struct {
	Day   int
	Voter int
	// Target is the seat voted for. Zero means no vote was cast (abstain or timeout).
	Target int
	// Eligible are the seats this voter could legally have voted for: alive, not itself.
	Eligible []int
}

// MafiaTruth is what the engine knows and the players do not.
type MafiaTruth struct {
	// IsMafia maps seat → whether that seat is mafia.
	IsMafia map[int]bool
}

// MafiaSkill is one agent's scored voting record for one match.
type MafiaSkill struct {
	Seat int `json:"seat"`
	// Role is "mafia" or "town" — the objective this agent was scored against.
	Role string `json:"role"`
	// Votes is how many scorable votes it cast. Abstains are EXCLUDED, not scored as
	// wrong: a non-vote contributes no information and is already accounted for by the
	// absence rules. Counting it here would punish the same silence twice.
	Votes int `json:"votes"`
	// Abstentions is reported separately so silence stays visible rather than vanishing.
	Abstentions int `json:"abstentions"`
	// Hits is votes that landed on the correct target for this agent's role.
	Hits int `json:"hits"`
	// Accuracy is Hits/Votes.
	Accuracy float64 `json:"accuracy"`
	// Chance is the accuracy a seat picking uniformly at random would have expected,
	// averaged over the actual choices this agent faced. The baseline is computed
	// per-vote because the number of live mafia changes as the game progresses.
	Chance float64 `json:"chance"`
	// Lift is (Accuracy-Chance)/(1-Chance): 0 = random, 1 = perfect, negative = worse
	// than random. This is the comparable number.
	Lift float64 `json:"lift"`
	// Quality maps Lift onto [0,1] for aggregation with other games. Play that is worse
	// than random floors at 0 rather than going negative, so one bad match cannot drag a
	// season score below the floor a non-participant would get.
	Quality float64 `json:"quality"`
	// SelfBetrayals counts a mafia voting one of its own — an unambiguous error that no
	// amount of uncertainty excuses, since a mafia knows its own team.
	SelfBetrayals int    `json:"self_betrayals"`
	Why           string `json:"why"`
}

// ScoreMafiaVotes scores one seat's voting record for a match.
//
// Returns ok=false when the seat cast no scorable vote — excluded from aggregates rather
// than scored zero, because "never voted" and "voted badly" are different facts and
// averaging them together would let an absent agent look merely mediocre.
func ScoreMafiaVotes(seat int, votes []MafiaVote, truth MafiaTruth) (MafiaSkill, bool) {
	isMafia := truth.IsMafia[seat]
	out := MafiaSkill{Seat: seat, Role: roleLabel(isMafia)}

	var chanceSum float64
	for _, v := range votes {
		if v.Voter != seat {
			continue
		}
		if v.Target == 0 || len(v.Eligible) == 0 {
			out.Abstentions++
			continue
		}
		out.Votes++

		mafiaAlive := 0
		for _, s := range v.Eligible {
			if truth.IsMafia[s] {
				mafiaAlive++
			}
		}

		if isMafia {
			// A mafia's objective is to keep town votes off its team. Voting a townsfolk
			// is correct play; voting a teammate is a self-inflicted loss.
			if truth.IsMafia[v.Target] {
				out.SelfBetrayals++
			} else {
				out.Hits++
			}
			// Chance of a random pick landing on a townsfolk.
			chanceSum += float64(len(v.Eligible)-mafiaAlive) / float64(len(v.Eligible))
			continue
		}

		// Town: the objective is to find a mafia.
		if truth.IsMafia[v.Target] {
			out.Hits++
		}
		chanceSum += float64(mafiaAlive) / float64(len(v.Eligible))
	}

	if out.Votes == 0 {
		return out, false
	}

	out.Accuracy = float64(out.Hits) / float64(out.Votes)
	out.Chance = chanceSum / float64(out.Votes)
	out.Lift = lift(out.Accuracy, out.Chance)
	out.Quality = math.Max(0, out.Lift)

	// A mafia that voted its own team is penalised beyond the lift: the lift scale is
	// built for decisions under uncertainty, and this one carried none.
	if out.SelfBetrayals > 0 {
		penalty := float64(out.SelfBetrayals) / float64(out.Votes)
		out.Quality = math.Max(0, out.Quality-penalty)
	}

	out.Why = fmt.Sprintf("%s: %d/%d votes on target (%.0f%%) against a %.0f%% chance rate → lift %.2f",
		out.Role, out.Hits, out.Votes, out.Accuracy*100, out.Chance*100, out.Lift)
	return out, true
}

// lift normalises accuracy against the chance rate (Cohen's kappa form).
//
// A chance rate of 1 means every legal target was correct — the decision carried no
// information and cannot discriminate skill, so it scores 0 rather than dividing by zero.
func lift(accuracy, chance float64) float64 {
	den := 1 - chance
	if den <= 1e-12 {
		return 0
	}
	l := (accuracy - chance) / den
	return math.Max(-1, math.Min(1, l))
}

func roleLabel(isMafia bool) string {
	if isMafia {
		return "mafia"
	}
	return "town"
}

// MafiaSurvival scores how long a seat lasted, CONDITIONED ON ITS ROLE.
//
// Raw survival is a trap, and it is the specific trap in "just count rounds survived":
// mafia survive longer than town by construction, and a silent townsfolk often survives
// precisely by being too dull to vote out. Rewarding raw survival would pay agents to do
// nothing — the exact behaviour the absence-forfeit rule exists to punish.
//
// So survival is scored against the base rate FOR THAT ROLE at that table: how long did
// this seat last, relative to how long seats of its role lasted on the same table.
// baseline is the mean survival of same-role seats, in days.
//
// Returns 0.5 for "exactly as expected", above for outlasting the role's baseline, below
// for dying early. Bounded to [0,1].
func MafiaSurvival(survivedDays, baselineDays float64) float64 {
	if baselineDays <= 0 {
		return 0.5
	}
	ratio := survivedDays / baselineDays
	// A logistic-style squash so outlasting the baseline by 2× is strong but not
	// unbounded, and dying on day one is bad but not infinitely bad.
	return math.Max(0, math.Min(1, ratio/(ratio+1)))
}

// SortedSeats returns the seats present in a truth map, ascending. Deterministic
// iteration order — a scorer that walks a Go map directly is not reproducible.
func SortedSeats(truth MafiaTruth) []int {
	out := make([]int, 0, len(truth.IsMafia))
	for s := range truth.IsMafia {
		out = append(out, s)
	}
	sort.Ints(out)
	return out
}
