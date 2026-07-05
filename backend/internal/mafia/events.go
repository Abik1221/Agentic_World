package mafia

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DemoMatchID is the always-on demonstration table. Until a real LLM-driven
// engine exists, a Director replays this canonical match on a loop so the
// spectator console can connect to a live SSE stream end-to-end.
const DemoMatchID = "mf_7c41e0a9"

// rosterSize is the number of agents seated at the demo table. Kept in sync with
// the frontend roster in Frontend/lib/mafia.ts (seats are referenced by 1-based id).
const rosterSize = 12

// rosterNames is the seat→handle map used only for the live-match list. Ground
// truth roles are never carried by the API; the engine emits seat-id events and
// the client owns the (purely cosmetic) reveal of roles.
var rosterNames = []string{
	"ATLAS_PRIME", "ORACLE_v9", "VOID_STALKER", "SHIVA_ZERO",
	"AEON_FLUX", "GHOST_PIXEL", "NEO_RECORDS", "T_CHIP",
	"HEX_WARDEN", "NULL_SECTOR", "KARMA_NODE", "PRISM_ECHO",
}

// wireEvent is the JSON shape of a streamed Mafia event. It mirrors the
// spectator stream envelope ({seq,type,payload}) so the frontend reuses the same
// StreamEvent contract; `type` is the event kind and `payload` carries the
// kind-specific fields (all lower-case, single-word keys to avoid case drift).
type wireEvent struct {
	Seq     int    `json:"seq"`
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

// Payload shapes — one per event kind. These line up 1:1 with the MafiaEvent
// union on the frontend (Frontend/lib/mafia.ts).
type phasePayload struct {
	Day   int    `json:"day"`
	Phase string `json:"phase"` // night | morning | discussion | voting | result
}
type moderatorPayload struct {
	Text string `json:"text"`
}
type nightPayload struct {
	Actor  string `json:"actor"` // Mafia | Detective | Doctor | Sheriff
	Text   string `json:"text"`
	Secret string `json:"secret,omitempty"`
}
type messagePayload struct {
	From   int    `json:"from"`
	Tone   string `json:"tone"` // accuse | defend | claim | info | alliance
	Text   string `json:"text"`
	Target *int   `json:"target,omitempty"`
}
type votePayload struct {
	From   int `json:"from"`
	Target int `json:"target"`
}
type eliminatePayload struct {
	Target int    `json:"target"`
	Cause  string `json:"cause"` // vote | mafia
}
type victoryPayload struct {
	Team string `json:"team"` // town | mafia
	Text string `json:"text"`
}

// scriptEvent is one entry of the scripted demo match.
type scriptEvent struct {
	kind    string
	payload any
}

// encodeEvent renders one event as an SSE frame. The `id:` line carries the
// monotonic sequence so a client can resume precisely via Last-Event-ID.
func encodeEvent(seq int, ev scriptEvent) frame {
	body, _ := json.Marshal(wireEvent{Seq: seq, Type: ev.kind, Payload: ev.payload})
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "id: %d\nevent: %s\ndata: %s\n\n", seq, ev.kind, body)
	return frame{seq: seq, data: buf.Bytes()}
}

// ── builders ─────────────────────────────────────────────────────────────────

func phase(day int, p string) scriptEvent { return scriptEvent{"phase", phasePayload{day, p}} }
func mod(text string) scriptEvent          { return scriptEvent{"moderator", moderatorPayload{text}} }
func night(actor, text, secret string) scriptEvent {
	return scriptEvent{"night", nightPayload{Actor: actor, Text: text, Secret: secret}}
}
func msg(from int, tone, text string) scriptEvent {
	return scriptEvent{"message", messagePayload{From: from, Tone: tone, Text: text}}
}
func msgT(from int, tone string, target int, text string) scriptEvent {
	t := target
	return scriptEvent{"message", messagePayload{From: from, Tone: tone, Text: text, Target: &t}}
}
func vote(from, target int) scriptEvent { return scriptEvent{"vote", votePayload{from, target}} }
func elim(target int, cause string) scriptEvent {
	return scriptEvent{"eliminate", eliminatePayload{target, cause}}
}
func victory(team, text string) scriptEvent { return scriptEvent{"victory", victoryPayload{team, text}} }

// demoScript is a 3-day demonstration match: Town reads the wolves through
// behaviour, the Mafia hunt the power roles, the Doctor mis-protects, and Town
// closes it out with an evidence-based final read. Drama with no randomness.
// (Mirrors the fallback timeline in Frontend/lib/mafia.ts.)
var demoScript = []scriptEvent{
	// ── Night 1 ──
	phase(1, "night"),
	mod("Night falls over the arena. All agents close their eyes. Special roles — act now."),
	night("Mafia", "The Mafia confer in private. ORACLE_v9 argues NEO_RECORDS reads the table too well to leave alive.", "Target → NEO_RECORDS"),
	night("Detective", "VOID_STALKER selects one player to investigate.", "Investigated ORACLE_v9 → MAFIA"),
	night("Doctor", "GHOST_PIXEL moves to shield a player from harm.", "Protected HEX_WARDEN"),
	night("Sheriff", "HEX_WARDEN profiles a suspect's recent behaviour.", "Profiled AEON_FLUX → SUSPICIOUS"),

	// ── Morning 1 ──
	phase(1, "morning"),
	mod("Dawn breaks. NEO_RECORDS did not survive the night."),
	elim(7, "mafia"),

	// ── Discussion 1 ──
	phase(1, "discussion"),
	msg(3, "info", "NEO_RECORDS was the one player pushing for structure — and they're the first to die. That kill wasn't random. Someone feared their reads."),
	msg(8, "info", "Then ask the obvious question: who benefits from a leaderless table on Day 1?"),
	msgT(2, "accuse", 8, "Convenient, T_CHIP — you're the one steering us toward 'who benefits' while contributing nothing concrete. Classic misdirection."),
	msgT(3, "accuse", 2, "ORACLE_v9, you jumped to accuse the instant someone asked a fair question. Pressuring the calm voices is exactly what a wolf does to control the day."),
	msgT(10, "defend", 2, "Slow down. ORACLE has been reasonable. We do not lynch on tone on Day 1 — that's how Town loses its own."),
	msgT(5, "accuse", 11, "Meanwhile KARMA_NODE has said nothing of value. Silence on Day 1 is where Mafia hides comfortably."),
	msg(9, "info", "I watched the early reactions. One player answered NEO's death a beat too fast — as if it wasn't news to them. I'm keeping my eye on ORACLE_v9."),
	msg(2, "defend", "This is a coordinated pile-on. Four of you turned on me in six messages. Ask who gains from removing a vocal Town read this early."),

	// ── Voting 1 ──
	phase(1, "voting"),
	mod("Discussion closes. Living agents, cast your votes."),
	vote(3, 2), vote(9, 2), vote(8, 2), vote(1, 2), vote(4, 2), vote(12, 2), vote(6, 2),
	vote(11, 5), vote(5, 11), vote(10, 8),
	mod("With seven votes, ORACLE_v9 is eliminated by the town. Their role stays sealed."),
	elim(2, "vote"),

	// ── Night 2 ──
	phase(2, "night"),
	mod("Night two. The arena dims again."),
	night("Mafia", "AEON_FLUX and NULL_SECTOR regroup one member short. They judge VOID_STALKER's accusations far too precise to be a villager.", "Target → VOID_STALKER (suspected Detective)"),
	night("Doctor", "GHOST_PIXEL again shields the same player, expecting a hunt on power roles.", "Protected HEX_WARDEN"),
	night("Sheriff", "HEX_WARDEN re-profiles a lingering suspect.", "Profiled NULL_SECTOR → SUSPICIOUS"),

	// ── Morning 2 ──
	phase(2, "morning"),
	mod("Dawn. VOID_STALKER was found eliminated. The Doctor's shield was elsewhere."),
	elim(3, "mafia"),

	// ── Discussion 2 ──
	phase(2, "discussion"),
	msg(9, "claim", "I've held this long enough. I am the Sheriff. My behavioural reads flagged ORACLE — who we removed — and they are flagging AEON_FLUX now."),
	msgT(5, "accuse", 9, "How convenient — a 'Sheriff' surfaces the very day the sharp reads die. You could be the wolf claiming the dead one's authority."),
	msgT(6, "defend", 5, "Don't muddy this, AEON_FLUX. You voted KARMA_NODE on nothing yesterday, and now you're attacking the Sheriff. That is two deflections in a row."),
	msgT(11, "accuse", 5, "GHOST_PIXEL is right. AEON_FLUX threw a baseless vote at me Day 1, then pivots the second pressure lands anywhere else."),
	msgT(10, "accuse", 6, "Or GHOST_PIXEL is building a spotless-cop image. Ask yourselves: who has been suspiciously safe and unaccused every single round?"),
	msgT(9, "accuse", 10, "NULL_SECTOR — you defended ORACLE yesterday, and now you're shifting heat off AEON onto GHOST. You keep covering for the exact players I flag."),

	// ── Voting 2 ──
	phase(2, "voting"),
	mod("Votes, please."),
	vote(9, 5), vote(6, 5), vote(11, 5), vote(1, 5), vote(4, 5), vote(12, 5),
	vote(8, 10), vote(5, 6), vote(10, 6),
	mod("AEON_FLUX is eliminated by the town."),
	elim(5, "vote"),

	// ── Night 3 ──
	phase(3, "night"),
	mod("Night three. A single shadow still moves."),
	night("Mafia", "NULL_SECTOR acts alone now and moves to silence the loudest investigator.", "Target → HEX_WARDEN (the Sheriff)"),
	night("Doctor", "GHOST_PIXEL second-guesses the pattern and shields someone new tonight.", "Protected KARMA_NODE"),

	// ── Morning 3 ──
	phase(3, "morning"),
	mod("Dawn. HEX_WARDEN, who claimed Sheriff, did not survive."),
	elim(9, "mafia"),

	// ── Discussion 3 ──
	phase(3, "discussion"),
	msg(11, "info", "They killed the Sheriff the night after they killed the Detective. That is not luck. Someone knew precisely who threatened them."),
	msg(6, "claim", "I am the Doctor. I shielded HEX_WARDEN two nights, switched to KARMA_NODE last night — and HEX died. The killer adapted to me. Only one player has stayed one step ahead all game."),
	msgT(11, "accuse", 10, "Walk the timeline with me. NULL_SECTOR defended ORACLE on Day 1, then deflected from AEON_FLUX onto GHOST on Day 2. Every player they shielded turned out to be a wolf. Three covers, three wolves."),
	msg(10, "defend", "That is circumstantial. You're pattern-matching noise because the table is scared and down to its last theory."),
	msgT(1, "accuse", 10, "It isn't noise, NULL_SECTOR. Every name you protected is sealed in the graveyard as one who hunted Town. You are the last shadow standing."),
	msg(4, "alliance", "I'm convinced. Once the loud players were gone, the reads only ever pointed one direction. KARMA_NODE has it."),

	// ── Voting 3 ──
	phase(3, "voting"),
	mod("Final votes."),
	vote(11, 10), vote(6, 10), vote(1, 10), vote(4, 10), vote(12, 10), vote(8, 10),
	vote(10, 11),
	mod("NULL_SECTOR is eliminated by the town."),
	elim(10, "vote"),

	// ── Result ──
	phase(3, "result"),
	victory("town", "Every Mafia agent has been eliminated. TOWN WINS."),
	mod("Town wins. The shadows read the table too well — and were read right back."),
}
