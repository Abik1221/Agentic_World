// Package clips manufactures shareable moments: it scans a finished match's event
// log for dramatic triggers, records clip rows, and renders preview assets on a
// bounded background worker pool — fully off the match hot path. See Stage 8.
package clips

import (
	"encoding/json"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
)

// Trigger kinds (see docs §9.3 dramatic triggers).
const (
	TieCarry    = "tie_carry"
	Comeback    = "comeback"
	PerfectRead = "perfect_read"
	AllIn       = "all_in"
	Blowout     = "blowout"
)

const (
	tieCarryPool    = 20 // a contested pot this large is dramatic
	comebackDeficit = 15 // trailed by this much then won
	blowoutScore    = 60 // winner reached this many of 91 points
)

// Trigger is one detected dramatic moment, pinned to the event seq that caused it.
type Trigger struct {
	Kind     string `json:"trigger"`
	RoundSeq int    `json:"round_seq"`
}

// Detect scans an ordered event log and returns the dramatic moments it contains,
// at most one per kind. Pure and deterministic — golden matches map to fixed
// triggers. Total rounds are read from the match_created event.
func Detect(events []gs.Event) []Trigger {
	totalRounds := 0
	type rd struct {
		seq, round, pool, cardA, cardB, winner, scoreA, scoreB int
	}
	var rounds []rd
	finishWinner, finishA, finishB, finishSeq := gs.Tie, 0, 0, 0
	haveFinish := false

	for _, ev := range events {
		switch ev.Type {
		case gs.EvMatchCreated:
			var p gs.MatchCreatedPayload
			if remarshal(ev.Payload, &p) {
				totalRounds = p.Rounds
			}
		case gs.EvRoundRevealed:
			var p gs.RoundRevealedPayload
			if remarshal(ev.Payload, &p) {
				rounds = append(rounds, rd{ev.Seq, p.Round, p.PrizePool, p.Cards[gs.SeatA], p.Cards[gs.SeatB], p.Winner, p.Scores[gs.SeatA], p.Scores[gs.SeatB]})
			}
		case gs.EvMatchFinished:
			var p gs.MatchFinishedPayload
			if remarshal(ev.Payload, &p) {
				finishWinner, finishA, finishB, finishSeq, haveFinish = p.Winner, p.Scores[gs.SeatA], p.Scores[gs.SeatB], ev.Seq, true
			}
		}
	}

	var out []Trigger
	tie, perfect, allin := false, false, false
	for _, r := range rounds {
		if !tie && r.pool >= tieCarryPool {
			out = append(out, Trigger{TieCarry, r.seq})
			tie = true
		}
		if !perfect && r.winner != gs.Tie {
			win, lose := r.cardA, r.cardB
			if r.winner == gs.SeatB {
				win, lose = r.cardB, r.cardA
			}
			if win == lose+1 {
				out = append(out, Trigger{PerfectRead, r.seq})
				perfect = true
			}
		}
		if !allin && totalRounds > 0 && r.round == totalRounds {
			out = append(out, Trigger{AllIn, r.seq})
			allin = true
		}
	}

	if haveFinish && finishWinner != gs.Tie {
		for _, r := range rounds {
			deficit := r.scoreB - r.scoreA
			if finishWinner == gs.SeatB {
				deficit = r.scoreA - r.scoreB
			}
			if deficit >= comebackDeficit {
				out = append(out, Trigger{Comeback, finishSeq})
				break
			}
		}
		winScore := finishA
		if finishWinner == gs.SeatB {
			winScore = finishB
		}
		if winScore >= blowoutScore {
			out = append(out, Trigger{Blowout, finishSeq})
		}
	}
	return out
}

// remarshal converts an event payload (typed struct when live, a map when loaded
// from the store) into the target struct via a JSON round-trip.
func remarshal(payload any, target any) bool {
	b, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, target) == nil
}
