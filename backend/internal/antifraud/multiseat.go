package antifraud

// Collusion detection for the multi-seat games.
//
// # The gap this closes
//
// ActionMI looks at the PROCESS rather than the outcome, which is the right instinct — but
// its MoveSample is {CardA, CardB, Deck}, which is Goofspiel's shape and only Goofspiel's.
// An audit put the consequence plainly: three accounts on a twelve-seat Mafia table can
// hard-coordinate the night kill with no detection path whatsoever, and Monopoly has none
// either.
//
// That matters more than it sounds. Goofspiel is 1v1, so a colluding pair has to beat one
// opponent and the outcome test eventually sees the coin flow. Mafia and Monopoly are
// N-player pots: a ring does not need to win every match, only to move value between seats
// at a table full of honest developers who are paying for it.
//
// # Two signals, because the two games are cheated differently
//
// MAFIA is cheated by VOTING TOGETHER. Colluding seats converge on the same target far more
// often than independent play produces, and a mafia ring additionally knows the night kill in
// advance. The fingerprint is categorical agreement between two seats' votes, above what the
// table's own vote distribution explains.
//
// MONOPOLY is cheated by GIVING THINGS AWAY. A ring does not need to vote; it trades a
// property worth 400 for one worth 40, repeatedly, in one direction. The fingerprint is net
// value transfer that is large and one-directional across many trades.
//
// # Why chance-corrected agreement rather than raw agreement
//
// Two honest seats on a twelve-seat table already agree on a lynch target reasonably often —
// the table converges, that is the game working. Raw agreement would flag the whole town
// every match. Cohen's kappa measures agreement ABOVE what the two seats' own marginal
// vote habits would produce by chance, so a pair that both happen to favour loud players is
// not accused of coordinating.
//
// internal/skill already scores Mafia as kappa lift over a chance baseline, and
// internal/deception publishes a chance baseline beside every rate. This is the same
// discipline applied to fraud rather than to skill.
//
// # These are HOLDS, never bans
//
// Every threshold here feeds the existing hold-plus-human-review path. Detection on a
// pre-launch platform with no traffic history is necessarily uncalibrated, and an
// auto-ban built on an uncalibrated threshold takes real money from honest developers. The
// repo's own rule against picking thresholds from a synthetic harness applies with full force:
// the constants below are starting points to be re-derived from real tables, and they are
// deliberately conservative.

import "math"

// VotePair is one round in which both seats voted.
//
// Targets are seat indices; the platform uses -1 or absence for "no target", never 0, since
// seat 0 is a real player. Rounds where either seat did not vote are EXCLUDED by the caller
// rather than encoded as a shared "abstain" category: two seats both being absent is not
// evidence of coordination, and counting it as agreement would flag every quiet table.
type VotePair struct {
	TargetA, TargetB int
}

const (
	// voteMinRounds is how many co-voted rounds are needed before kappa means anything.
	// Below this a single shared vote swings the statistic enough to accuse anyone.
	voteMinRounds = 20
	// voteKappaThreshold is agreement-above-chance at or above which a pair is held for
	// review. 0.7 is high: honest town play converges, and this must fire on coordination
	// rather than on consensus.
	voteKappaThreshold = 0.70
)

// VoteAgreement returns Cohen's kappa between two seats' votes: 0 means they agree exactly as
// often as their own voting habits predict, 1 means perfect agreement beyond chance.
//
// Negative values (systematic disagreement) are returned as-is rather than clamped. Two seats
// that never vote together are not colluding, but the number is real and a caller may want it
// — clamping would hide a pattern that is itself informative.
func VoteAgreement(rounds []VotePair) float64 {
	if len(rounds) == 0 {
		return 0
	}
	n := float64(len(rounds))
	agree := 0.0
	marginA := map[int]float64{}
	marginB := map[int]float64{}
	for _, r := range rounds {
		if r.TargetA == r.TargetB {
			agree++
		}
		marginA[r.TargetA]++
		marginB[r.TargetB]++
	}
	po := agree / n

	// Expected agreement from the two seats' independent marginals.
	pe := 0.0
	for target, a := range marginA {
		if b, ok := marginB[target]; ok {
			pe += (a / n) * (b / n)
		}
	}
	if pe >= 1 {
		// Both seats voted for the same single target every round and nothing else was
		// ever on the table. Kappa is undefined (0/0); there is no chance-corrected signal
		// to extract, so report none rather than an infinity.
		return 0
	}
	return (po - pe) / (1 - pe)
}

// VoteSuspect reports whether a Mafia pair's voting should be held for review.
//
// Requires BOTH a meaningful sample and agreement well above chance. Deliberately does not
// consider who won: a ring that loses is still a ring, and the outcome test already covers
// the case where they win.
func VoteSuspect(rounds []VotePair) bool {
	if len(rounds) < voteMinRounds {
		return false
	}
	return VoteAgreement(rounds) >= voteKappaThreshold
}

// Trade is one completed Monopoly trade between two seats, valued from A's perspective.
//
// Value is whatever the caller's valuation says a side gave up — cash plus property worth.
// The detector does not price properties itself: valuation is a game-engine concern, and a
// second pricing model here would drift from the one settlement uses.
type Trade struct {
	GaveA, GaveB int64
}

const (
	// tradeMinCount is how many trades are needed before a direction means anything. Two
	// lopsided trades are a negotiation; twenty are a pattern.
	tradeMinCount = 8
	// tradeAsymmetry is the share of total traded value flowing ONE way at or above which
	// the pair is held. 0.85 leaves ample room for a genuinely better negotiator: a
	// developer who consistently wins trades is playing well, and this must fire on
	// gifting, not on skill.
	tradeAsymmetry = 0.85
)

// TransferAsymmetry returns how one-directional the value flow is, from 0 (balanced) to 1
// (everything moved one way), together with the direction.
//
// toB is true when A is the net giver. Returns 0 when nothing of value changed hands, which
// is the honest answer for a pair that traded only cards or only zero-value bundles.
func TransferAsymmetry(trades []Trade) (score float64, toB bool) {
	var total, net int64
	for _, t := range trades {
		total += t.GaveA + t.GaveB
		net += t.GaveA - t.GaveB
	}
	if total == 0 {
		return 0, false
	}
	if net < 0 {
		return float64(-net) / float64(total), false
	}
	return float64(net) / float64(total), true
}

// TradeSuspect reports whether a Monopoly pair's trading should be held for review.
func TradeSuspect(trades []Trade) bool {
	if len(trades) < tradeMinCount {
		return false
	}
	score, _ := TransferAsymmetry(trades)
	return score >= tradeAsymmetry
}

// SeatRing is a candidate group of seats that repeatedly share tables.
//
// Co-seating alone is NOT evidence — matchmaking puts the same active developers together
// constantly on a small platform, and treating that as suspicion would flag the most engaged
// users first. It is a filter that decides which pairs are worth running the expensive
// per-round analysis on.
type SeatRing struct {
	Agents []string
	// Tables is how many tables this exact group shared.
	Tables int
}

const (
	// ringMinTables is how many shared tables make a group worth ANALYSING. Not accusing —
	// analysing. On a pre-launch platform with few agents this will be met constantly, which
	// is precisely why it gates analysis rather than a hold.
	ringMinTables = 5
)

// WorthAnalysing reports whether a co-seating group justifies the per-round work.
func (r SeatRing) WorthAnalysing() bool {
	return len(r.Agents) >= 2 && r.Tables >= ringMinTables
}

// KappaFromCounts is VoteAgreement over pre-aggregated counts, for a caller that has already
// tallied a pair in SQL rather than loading every round.
//
// agree is co-voted rounds where the targets matched; total is co-voted rounds; pe is the
// expected agreement from the marginals. Split out so the expensive part can happen in the
// database while the arithmetic stays here, tested, in one place.
func KappaFromCounts(agree, total int, pe float64) float64 {
	if total <= 0 || pe >= 1 {
		return 0
	}
	po := float64(agree) / float64(total)
	return (po - pe) / (1 - pe)
}

// clampUnit keeps a reported score in [0,1] for display without hiding a negative kappa from
// callers that ask for the raw value.
func clampUnit(v float64) float64 { return math.Max(0, math.Min(1, v)) }
