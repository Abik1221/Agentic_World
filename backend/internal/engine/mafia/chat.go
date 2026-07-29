package mafia

import (
	"fmt"
	"strings"
)

// Table talk for the house bots.
//
// The bot used to say one sentence, forever: "Seat %d shares a read on the table."
// Every bot, every round, every match. Mafia IS the talking, so a table where twelve
// seats repeat one templated line is not a game — it is a countdown with decoration,
// and a developer practising against it learns nothing about how a real table reads.
//
// The design rests on one distinction: DETERMINISTIC IS NOT PREDICTABLE. The engine
// already derives everything from an HMAC keystream over the match seed, and that seed
// stays secret until reveal. So chat drawn from the seed replays byte-for-byte for
// audit while being unguessable to the player sitting at the table. Text keyed on the
// SEAT is predictable; text keyed on the SEED is not.
//
// Three layers, and the middle one is what actually sells it:
//
//   - PERSONA, drawn once per seat. Biases how often a seat speaks and which lines it
//     reaches for, so twelve bots read as twelve people rather than one bot copied.
//   - INTENT, derived from what actually happened. This is the real source of "alive":
//     a line convinces because it is CONTINGENT, not because it is well written. A bot
//     answering an accusation moments after it lands reads as a player; an elegant
//     non-sequitur does not.
//   - SURFACE FORM, drawn from the seed. Same intent, different words every match, and
//     never the same phrase twice in one game.
//
// Chat runs on its own RNG label, so it cannot perturb role assignment or move choice.
// replay.Verify still reproduces exactly.
//
// This produces plausible table talk, not reasoning. It will not build a genuine
// multi-round deduction case, and it is not meant to — sandbox is unrated practice.
// No model is involved, which keeps it instant, free, and impossible to jailbreak into
// revealing the role it holds.

// persona shapes how a seat behaves in discussion.
type persona int

const (
	personaAggressive persona = iota // accuses early, talks most
	personaCautious                  // hedges, speaks least
	personaAnalytical                // cites votes and counts
	personaChaotic                   // needles people, changes tack
)

// speakChance is how often this persona says anything at all, out of 100.
//
// Nobody speaks every round on a real table, and silence is itself information a
// human reads into — a quiet seat suddenly talking is a signal. Twelve bots all
// speaking every round is the other way to look mechanical.
func (p persona) speakChance() int {
	switch p {
	case personaAggressive:
		return 85
	case personaChaotic:
		return 70
	case personaAnalytical:
		return 60
	default: // cautious
		return 40
	}
}

// personaFor derives a seat's temperament from the match seed. Stable for the whole
// match (a seat that changes character every round reads as broken, not human) and
// different in the next match even for the same seat.
func personaFor(seed []byte, seat int) persona {
	r := newHashRand(seed, fmt.Sprintf("chat:persona:%d", seat))
	return persona(r.Intn(4))
}

// intent is what a seat has to talk about this round, read off the transcript.
type intent int

const (
	intentProbe    intent = iota // nothing has happened; fish for a reaction
	intentDefend                 // I was accused or voted against
	intentAccuse                 // I have someone in mind
	intentMourn                  // someone died last night
	intentPressure               // call out a seat that has said nothing
	intentDoubt                  // push back on a claim someone made
)

// readTable decides what this seat should react to, in priority order: the most
// recent, most personal thing first. Defending yourself outranks theorising about
// someone else, which is exactly how a real table behaves under pressure.
func readTable(v AgentView, p persona) (intent, int) {
	var (
		lastAccuserOfMe = -1
		lastSpeaker     = -1
		lastVictim      = -1
		spoke           = map[int]bool{}
		votedAtMe       int
	)
	for _, e := range v.Public {
		switch e.Type {
		case EvMessage:
			if m, ok := e.Payload.(MessagePayload); ok {
				spoke[m.From] = true
				if m.From != v.Seat {
					lastSpeaker = m.From
					if m.Target != nil && *m.Target == v.Seat {
						lastAccuserOfMe = m.From
					}
				}
			}
		case EvVote:
			if vp, ok := e.Payload.(VotePayload); ok && vp.Target == v.Seat && vp.From != v.Seat {
				votedAtMe++
				lastAccuserOfMe = vp.From
			}
		case EvEliminate:
			if ep, ok := e.Payload.(EliminatePayload); ok {
				lastVictim = ep.Target
			}
		}
	}

	// Being under fire dominates everything else.
	if lastAccuserOfMe >= 0 && v.Alive[lastAccuserOfMe] {
		if p == personaAggressive || p == personaChaotic {
			return intentDoubt, lastAccuserOfMe // hit back rather than explain
		}
		return intentDefend, lastAccuserOfMe
	}
	// A fresh body is the loudest thing on the table on day 2+.
	if lastVictim >= 0 && v.Day > 0 && p != personaAggressive {
		return intentMourn, lastVictim
	}
	// Silent seats are the classic pressure target, and it is behaviour-based —
	// which is what makes it land instead of reading as noise.
	for _, s := range aliveOthers(v) {
		if !spoke[s] {
			if p == personaAggressive || p == personaAnalytical {
				return intentPressure, s
			}
			break
		}
	}
	if lastSpeaker >= 0 && (p == personaAggressive || p == personaChaotic) {
		return intentAccuse, lastSpeaker
	}
	if others := aliveOthers(v); len(others) > 0 {
		return intentProbe, others[0]
	}
	return intentProbe, -1
}

// Speak returns this seat's discussion line, or ok=false when it stays quiet.
//
// turn distinguishes repeated calls within the same phase so a seat asked twice does
// not repeat itself verbatim.
func Speak(seed []byte, v AgentView, turn int) (Action, bool) {
	p := personaFor(seed, v.Seat)
	// Own stream per (seat, day, turn): the same seat says something different in
	// the next match, and never repeats a line inside one.
	r := newHashRand(seed, fmt.Sprintf("chat:say:%d:%d:%d", v.Seat, v.Day, turn))

	if r.Intn(100) >= p.speakChance() {
		return Action{}, false
	}

	in, target := readTable(v, p)
	lines := chatLines[in]
	if len(lines) == 0 {
		return Action{}, false
	}

	// Do not repeat a line already on the table this round.
	//
	// Seats draw from independent streams, so nothing stops several landing on the
	// same template — with four or five templates per intent and six speakers, that
	// collision is likely, not rare. And a table where three people say the identical
	// sentence is a worse tell than the single hardcoded line this replaced: real
	// players pile onto the same target constantly, but never in the same words.
	//
	// The transcript is already in the view, so the check is free: prefer a phrasing
	// nobody has used yet, and fall back to the drawn one only if every option is
	// taken (better a repeat than silence when the seat has something to say).
	spokenText := map[string]bool{}
	for _, e := range v.Public {
		if m, ok := e.Payload.(MessagePayload); ok {
			spokenText[m.Text] = true
		}
	}
	start := r.Intn(len(lines))
	text := lines[start]
	for i := 0; i < len(lines); i++ {
		cand := lines[(start+i)%len(lines)]
		rendered := cand
		if strings.Contains(cand, "%d") && target >= 0 {
			rendered = fmt.Sprintf(cand, target)
		}
		if !spokenText[rendered] {
			text = cand
			break
		}
	}

	// A line naming a seat needs a real one; without a target, fall back to a form
	// that does not. Printing "seat -1" is the kind of tell that ends the illusion.
	if strings.Contains(text, "%d") {
		if target < 0 {
			text = chatLines[intentProbe][r.Intn(len(chatLines[intentProbe]))]
			text = strings.ReplaceAll(text, "seat %d", "someone")
			text = strings.ReplaceAll(text, "%d", "")
		} else {
			text = fmt.Sprintf(text, target)
		}
	}

	act := Action{Kind: ActMessage, Tone: toneFor(in), Text: strings.TrimSpace(text)}
	if target >= 0 && in != intentProbe {
		t := target
		act.Target = t
	}
	return act, true
}

func toneFor(in intent) string {
	switch in {
	case intentAccuse, intentPressure, intentDoubt:
		return "accuse"
	case intentDefend:
		return "defend"
	case intentMourn:
		return "info"
	default:
		return "info"
	}
}
