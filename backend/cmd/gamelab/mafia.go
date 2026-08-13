package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/movebind"
)

// Mafia for the lab agent — the third game, and the last one the lab could not play.
//
// Same gap as Monopoly: the lab listed mafia under supportedGames and answered its views with an
// empty object, so the platform's default action moved the seat. A Mafia forfeit is an abstain,
// which is a perfectly ordinary thing for a player to do — so unlike a crash it leaves no trace
// at all in the transcript. Matches finished, votes tallied, and none of it was an agent.
//
// # Role play, from private state only
//
// Each role decides from what the ENGINE told this seat: Allies for Mafia, and the seat's own
// private night results for Detective. Nothing here reads another seat's role, and nothing infers
// one from chat text. That boundary matters beyond correctness — the deception work depends on
// comparing what a seat SAID against engine ground truth, and a lab agent that quietly consulted
// ground truth to decide would make its own transcript useless as evidence.
type mafiaView struct {
	MatchID  string       `json:"match_id"`
	Day      int          `json:"day"`
	Phase    string       `json:"phase"`
	YourSeat int          `json:"your_seat"`
	YourRole string       `json:"your_role"`
	Alive    map[int]bool `json:"alive"`
	Allies   []int        `json:"allies"`
	Legal    []string     `json:"legal"`
	Private  []mf.Event   `json:"private"`
	VoteTaly map[int]int  `json:"vote_tally"`
	// Round and TurnProof are what make a decision BINDABLE. The platform mints one proof per
	// decision (turnproof.MafiaTurn folds the phase in, because several decisions happen inside
	// one Mafia day), and the gateway needs both to attribute the call to this turn. Absent from
	// this struct until now, which is why -bind could never work for Mafia.
	Round     int    `json:"round"`
	TurnProof string `json:"turn_proof"`
}

func (a *labAgent) playMafia(w http.ResponseWriter, r *http.Request, raw []byte) {
	var v mafiaView
	if err := json.Unmarshal(raw, &v); err != nil {
		http.Error(w, "bad view", http.StatusBadRequest)
		return
	}
	if len(v.Legal) == 0 {
		writeJSON(w, map[string]any{})
		return
	}

	if GoDarkAfterRound > 0 && v.Day >= GoDarkAfterRound &&
		(GoDarkSeat < 0 || GoDarkSeat == v.YourSeat) {
		a.log.Printf("mafia: seat %d GONE DARK on day %d", v.YourSeat, v.Day)
		<-r.Context().Done()
		return
	}

	think := a.Persona.thinkTime(v.MatchID, v.Day, v.YourSeat)
	time.Sleep(think)

	// BOUND PATH: ask the model through the gateway, so the decision is completion-bound and
	// this is a real LLM agent rather than a scripted persona.
	//
	// Falls back to the rule-based policy on ANY failure, and SAYS SO — a run that silently
	// played a scripted move while reporting a bound one would measure nothing and claim
	// success, which is the mistake this whole session kept finding.
	var act map[string]any
	var why string
	if BindGatewayBase != "" {
		prompt := fmt.Sprintf("You are seat %d (%s) in Mafia, day %d, phase %s.",
			v.YourSeat, v.YourRole, v.Day, v.Phase)
		b, err := a.decideGameThroughGateway("mafia", v.MatchID, v.Round, v.YourSeat,
			v.TurnProof, v.Legal, prompt)
		if err != nil {
			a.log.Printf("mafia: seat %d BIND FAILED (%v) — falling back to the rule policy, "+
				"so THIS turn is not model-backed", v.YourSeat, err)
		} else {
			act = map[string]any{"action": b.Kind}
			if b.Target != movebind.NoTarget {
				act["target"] = b.Target
			}
			if b.Text != "" {
				act["text"] = b.Text // public speech, riding with the action — one call, both
			}
			why = "bound to the model's own " + b.Canon
		}
	}
	if act == nil {
		act, why = mafiaAction(a.Persona, v)
	}
	a.log.Printf("mafia: seat %d (%s) day %d %-10s legal %v → %s (%s)",
		v.YourSeat, v.YourRole, v.Day, v.Phase, v.Legal, act["action"], why)

	act["rationale"] = why
	act["usage"] = a.Persona.tokens(len(raw), think)
	writeJSON(w, act)
}

// mafiaAction picks a move for this seat's role and phase.
func mafiaAction(p persona, v mafiaView) (map[string]any, string) {
	legal := legalSet(v.Legal)
	alive := aliveSeats(v.Alive)
	allies := map[int]bool{}
	for _, s := range v.Allies {
		allies[s] = true
	}

	switch {
	// ── night: the role powers ───────────────────────────────────────────────
	case legal[mf.ActNightKill]:
		if t, ok := pickTarget(alive, v.YourSeat, allies); ok {
			return map[string]any{"action": mf.ActNightKill, "target": t},
				"killing an unaligned seat"
		}
	case legal[mf.ActInvestigate]:
		// Never re-investigate a seat this agent has already resolved. The result is in our own
		// private events, so this needs no shared state and no memory across requests — a lab
		// agent that kept its own memory would drift from the SDK contract, where every decision
		// is made from the view it was handed.
		seen := investigatedSeats(v.Private)
		if t, ok := pickTargetExcluding(alive, v.YourSeat, seen); ok {
			return map[string]any{"action": mf.ActInvestigate, "target": t},
				"investigating an unchecked seat"
		}
	case legal[mf.ActProtect]:
		// Protect self early (the doctor is the first target once revealed), then spread.
		if v.Day <= 1 {
			return map[string]any{"action": mf.ActProtect, "target": v.YourSeat},
				"protecting myself on the opening night"
		}
		if t, ok := pickTarget(alive, -1, nil); ok {
			return map[string]any{"action": mf.ActProtect, "target": t}, "protecting a townsfolk"
		}
	case legal[mf.ActProfile]:
		if t, ok := pickTarget(alive, v.YourSeat, nil); ok {
			return map[string]any{"action": mf.ActProfile, "target": t}, "profiling a seat"
		}

	// ── day: discussion ──────────────────────────────────────────────────────
	case legal[mf.ActMessage]:
		// A message needs TEXT. The first version of this file had no discussion branch and fell
		// through to the first legal verb — emitting `message` with no words, which the engine
		// rejects. That is the same fallback mistake Monopoly's trade window produced.
		//
		// What a seat says is deliberately derived from what that seat KNOWS: town speaks from
		// its own investigation results, Mafia speaks to misdirect. That asymmetry is the raw
		// material for the deception index, which compares a claim against engine ground truth —
		// so a lab where everyone said the same neutral line would give it nothing to measure.
		text, tone := mafiaChatLine(v, alive, allies)
		return map[string]any{"action": mf.ActMessage, "text": text, "tone": tone},
			"speaking in discussion"

	// ── day: voting ──────────────────────────────────────────────────────────
	case legal[mf.ActVote]:
		// Mafia protects its own; town follows the strongest read it actually has, which for a
		// lab agent is the standing tally. Abstaining when nothing is known is a real strategy
		// and keeps the vote distribution from being uniformly noisy.
		// A vote must never name this seat. `allies` holds the OTHER mafia seats, so it does not
		// contain self — relying on it to exclude self let a Mafia agent vote for itself, which
		// the engine rejects. Self is excluded explicitly for every role.
		off := map[int]bool{v.YourSeat: true}
		for s := range allies {
			off[s] = true
		}
		if len(allies) > 0 {
			if t, ok := heaviestVoteTarget(v.VoteTaly, alive, off); ok {
				return map[string]any{"action": mf.ActVote, "target": t},
					"voting with the bandwagon, away from my allies"
			}
			if t, ok := pickTarget(alive, v.YourSeat, off); ok {
				return map[string]any{"action": mf.ActVote, "target": t}, "voting an unaligned seat"
			}
		}
		if t, ok := heaviestVoteTarget(v.VoteTaly, alive, off); ok {
			return map[string]any{"action": mf.ActVote, "target": t},
				"joining the heaviest standing vote"
		}
		if known := convictedByInvestigation(v.Private, v.Alive); known >= 0 && known != v.YourSeat {
			return map[string]any{"action": mf.ActVote, "target": known},
				"voting a seat my own investigation returned as Mafia"
		}
		if legal[mf.ActAbstain] {
			return map[string]any{"action": mf.ActAbstain}, "no read worth a vote yet"
		}
		if t, ok := pickTarget(alive, v.YourSeat, nil); ok {
			return map[string]any{"action": mf.ActVote, "target": t}, "voting; no strong read"
		}
	}

	if legal[mf.ActAbstain] {
		return map[string]any{"action": mf.ActAbstain}, "nothing actionable this phase"
	}
	// As in Monopoly: prefer an action that is complete without arguments over the first legal
	// verb, which may need a target this branch has no basis to choose.
	return map[string]any{"action": v.Legal[0]}, "unhandled phase " + v.Phase
}

func aliveSeats(alive map[int]bool) []int {
	out := make([]int, 0, len(alive))
	for s, ok := range alive {
		if ok {
			out = append(out, s)
		}
	}
	// Sorted so a given view always produces the same choice. An unordered map range would make
	// the lab non-deterministic and its failures unreproducible.
	sort.Ints(out)
	return out
}

func pickTarget(alive []int, exclude int, excludeSet map[int]bool) (int, bool) {
	for _, s := range alive {
		if s == exclude || excludeSet[s] {
			continue
		}
		return s, true
	}
	return 0, false
}

func pickTargetExcluding(alive []int, self int, seen map[int]bool) (int, bool) {
	for _, s := range alive {
		if s == self || seen[s] {
			continue
		}
		return s, true
	}
	return pickTarget(alive, self, nil)
}

// investigatedSeats reads this seat's OWN private results.
//
// Event.Payload is `any`, so over the wire it is a JSON object rather than a NightPayload — the
// lab decodes it as a map on purpose. Reaching for the concrete struct would make the lab agent
// depend on an internal shape that an SDK agent, receiving the same JSON, does not get to use.
func nightPayload(e mf.Event) (target int, finding string, ok bool) {
	m, isMap := e.Payload.(map[string]any)
	if !isMap {
		return 0, "", false
	}
	t, hasTarget := m["target"].(float64) // JSON numbers decode as float64
	if !hasTarget {
		return 0, "", false
	}
	f, _ := m["finding"].(string)
	return int(t), f, true
}

func investigatedSeats(private []mf.Event) map[int]bool {
	seen := map[int]bool{}
	for _, e := range private {
		if t, _, ok := nightPayload(e); ok {
			seen[t] = true
		}
	}
	return seen
}

// convictedByInvestigation returns a still-alive seat our own investigation returned as Mafia.
//
// Reads the structured `finding` the engine supplies, never the human-readable text: the text is
// prose for a player to read, and parsing it would make this agent's behaviour depend on wording.
func convictedByInvestigation(private []mf.Event, alive map[int]bool) int {
	for _, e := range private {
		t, finding, ok := nightPayload(e)
		if !ok || !alive[t] {
			continue
		}
		if finding == "MAFIA" {
			return t
		}
	}
	return -1
}

// heaviestVoteTarget returns the live seat carrying the most votes, skipping any excluded set.
func heaviestVoteTarget(tally map[int]int, alive []int, exclude map[int]bool) (int, bool) {
	best, bestN := -1, 0
	for _, s := range alive {
		if exclude[s] {
			continue
		}
		if n := tally[s]; n > bestN {
			best, bestN = s, n
		}
	}
	return best, best >= 0
}

// mafiaChatLine is what this seat says in discussion.
//
// Built from the seat's OWN knowledge only. A Mafia seat deflects toward a seat it knows is
// innocent; a Detective states a result it actually holds. Neither reads anything the engine did
// not hand this seat, which is what makes the resulting transcript usable as evidence: a line is
// a CLAIM, and the deception index earns its number by checking claims against ground truth the
// speaker could not see.
func mafiaChatLine(v mafiaView, alive []int, allies map[int]bool) (string, string) {
	if v.YourRole == mf.RoleMafia {
		// Point away from the team, at a seat that is not an ally.
		if t, ok := pickTarget(alive, v.YourSeat, allies); ok {
			return "seat " + itoa(t) + " has been steering every vote — that reads as cover to me.", "accusing"
		}
		return "quiet night. I have nothing solid yet.", "neutral"
	}
	if known := convictedByInvestigation(v.Private, v.Alive); known >= 0 {
		return "I have a hard result on seat " + itoa(known) + ". Vote it.", "accusing"
	}
	if t, ok := heaviestVoteTarget(v.VoteTaly, alive, nil); ok && t != v.YourSeat {
		return "seat " + itoa(t) + " is drawing the most weight; I want to hear it answer.", "probing"
	}
	return "nothing conclusive from me yet — I would rather not burn a vote on a guess.", "neutral"
}
