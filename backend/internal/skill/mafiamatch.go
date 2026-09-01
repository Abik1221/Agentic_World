package skill

import "sort"

// Wiring Mafia into the scoring pipeline.
//
// # Why this file exists, and why Mafia was invisible without it
//
// internal/skill/mafia.go has scored Mafia correctly since the day it was written, and had
// never once been called. The worker dispatches on game with a case for goofspiel, a case for
// monopoly and a default that returns "unscorable" — Mafia fell into the default. It was then
// STAMPED with the scorer version, which is what makes the omission permanent rather than
// merely current: the stamp exists so the worker never re-reads a row it has judged, so every
// Mafia decision was marked done and never looked at again.
//
// The cost was the whole dimension. Mafia is the platform's highest-volume game — 4.2 million
// recorded decisions, of which 1.6 million are votes — and every one of them contributed
// nothing to Decision Quality. A dimension that silently covers one game out of three is worse
// than one that covers none, because the gap is invisible: the score renders, it is simply
// blind to most of what was played.
//
// # Why Mafia cannot be scored one decision at a time
//
// The per-decision path could not have been made to work as the data stands, for two
// independent reasons, and both are properties of the game rather than of the schema:
//
//   - The decision row does not record WHAT was voted. `action` is the bare string "vote";
//     the target lives in the match event log as {"from": seat, "target": seat}.
//   - Lift is undefined for a single vote. It measures accuracy against the chance rate a
//     random voter would have achieved, and one vote has an accuracy of either 0 or 1. A
//     sample of one cannot separate a good agent from a lucky one, which is precisely the
//     confusion the metric exists to remove.
//
// So Mafia is scored per MATCH-SEAT, which is the smallest unit on which its statistic is
// defined, and the seat's result is then attributed back to the votes it actually cast.
//
// # Reconstructing who could be voted for
//
// Lift needs the ELIGIBLE set at the moment of each vote, because the chance rate depends on
// how many mafia were still alive among the seats available. That is not stored per vote, so
// the event log is replayed in sequence: every seat starts alive, an `eliminate` event removes
// one, and a vote's eligible set is whoever is alive except the voter. Replaying is not a
// convenience — reading the FINAL alive set instead would compute every early vote against a
// late-game chance rate and quietly reward agents whose matches ran long.

// MafiaSeatRow is one seat's identity and the ground truth for it.
type MafiaSeatRow struct {
	Seat    int
	AgentID int64
	IsMafia bool
}

// MafiaEventRow is one ordered entry from the match event log.
//
// Only vote and eliminate carry information this scorer needs; everything else advances the
// sequence. Day is carried so a vote can be attributed to the right day.
type MafiaEventRow struct {
	Seq    int
	Type   string
	Day    int
	From   int
	Target int
	// Seat is the seat an `eliminate` event removes.
	Seat int
}

// MafiaMatchInput is one complete match, ready to score.
type MafiaMatchInput struct {
	MatchID string
	Seats   []MafiaSeatRow
	Events  []MafiaEventRow
}

// MafiaSeatScore is one seat's verdict.
type MafiaSeatScore struct {
	AgentID int64
	Seat    int
	Skill   MafiaSkill
	// Regret is 1-Quality, so it shares a direction and a scale with the other games'
	// per-decision regret and can be averaged with them. Quality is already floored at 0
	// and capped at 1, so this is bounded without further clamping.
	Regret float64
}

// ScoreMafiaMatch replays one match and scores every seat that cast a scorable vote.
//
// Seats that never voted are omitted rather than scored zero — the same rule ScoreMafiaVotes
// applies, for the same reason: "never voted" and "voted badly" are different facts, and
// averaging them together lets an absent agent look merely mediocre.
func ScoreMafiaMatch(in MafiaMatchInput) []MafiaSeatScore {
	truth := MafiaTruth{IsMafia: make(map[int]bool, len(in.Seats))}
	alive := make(map[int]bool, len(in.Seats))
	agentOf := make(map[int]int64, len(in.Seats))
	for _, s := range in.Seats {
		truth.IsMafia[s.Seat] = s.IsMafia
		alive[s.Seat] = true
		agentOf[s.Seat] = s.AgentID
	}

	evs := make([]MafiaEventRow, len(in.Events))
	copy(evs, in.Events)
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].Seq < evs[j].Seq })

	byVoter := map[int][]MafiaVote{}
	for _, e := range evs {
		switch e.Type {
		case "vote":
			// Eligible is computed BEFORE applying any later elimination, which is why the
			// replay order matters.
			eligible := make([]int, 0, len(alive))
			for seat, ok := range alive {
				if ok && seat != e.From {
					eligible = append(eligible, seat)
				}
			}
			sort.Ints(eligible)
			byVoter[e.From] = append(byVoter[e.From], MafiaVote{
				Day: e.Day, Voter: e.From, Target: e.Target, Eligible: eligible,
			})
		case "eliminate":
			delete(alive, e.Seat)
		}
	}

	out := make([]MafiaSeatScore, 0, len(byVoter))
	for _, s := range in.Seats {
		votes := byVoter[s.Seat]
		if len(votes) == 0 {
			continue
		}
		sk, ok := ScoreMafiaVotes(s.Seat, votes, truth)
		if !ok {
			continue
		}
		out = append(out, MafiaSeatScore{
			AgentID: agentOf[s.Seat], Seat: s.Seat, Skill: sk, Regret: 1 - sk.Quality,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seat < out[j].Seat })
	return out
}
