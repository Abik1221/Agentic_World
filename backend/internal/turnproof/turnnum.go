package turnproof

// Turn numbering for games whose state has no single monotonic turn counter.
//
// A proof binds a gateway LLM call to ONE decision, identified by (agent, match, round).
// Goofspiel and Monopoly each already have a natural round/turn integer. Mafia does not:
// its clock is a Day plus a Phase, and several decisions happen inside one day (discuss,
// vote, then the night action). Numbering those all as "day 3" would let a single proof
// cover every decision in that day — and since a bound decision is recorded once per
// (match, agent, round), it would also collapse them into one count.
//
// MafiaTurn derives a stable, strictly increasing number from (day, phase) so each
// decision has its own identity. It must be DETERMINISTIC and shared by the minting side
// and anything that later reasons about the number, which is why it lives here next to the
// signer rather than inside the Mafia package.

// mafiaPhaseOrder is the within-day ordering. Unknown phases sort last rather than
// colliding with a known one: a new phase added to the engine should get its own slot the
// first time it is seen, not silently share another phase's proof.
var mafiaPhaseOrder = map[string]int{
	"night":      0,
	"reveal":     1,
	"discussion": 2,
	"discuss":    2,
	"voting":     3,
	"vote":       3,
	"trial":      4,
	"defense":    5,
	"verdict":    6,
}

// mafiaPhaseSlots is the stride per day. Wider than the number of known phases so adding
// one does not renumber existing days — a renumbering would invalidate proofs mid-match.
const mafiaPhaseSlots = 16

// MafiaTurn maps a Mafia (day, phase) to a unique turn number.
//
// Day is 1-based in the engine; day 0 (pre-start) maps below day 1 without going negative.
func MafiaTurn(day int, phase string) int {
	if day < 0 {
		day = 0
	}
	slot, ok := mafiaPhaseOrder[phase]
	if !ok {
		// Unknown phase: park it at the top of the day's range so it cannot be mistaken
		// for a known phase's turn.
		slot = mafiaPhaseSlots - 1
	}
	return day*mafiaPhaseSlots + slot
}
