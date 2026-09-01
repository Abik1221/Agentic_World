package mafia

import (
	"fmt"
	"sort"
)

// agent.go provides the simulated players that fill the seats a human does not
// occupy, so a single user's agent can play a full Mafia match against a believable
// table. Bots see ONLY their redacted AgentView (their own role, their allies if
// Mafia, the public transcript, and their own night results) — exactly what a real
// external agent receives — and they always submit LEGAL actions.
//
// Bots are pure and deterministic: their randomness is a seed-derived stream keyed
// by their seat, so a table replays identically.

// Agent decides one action from a redacted view. A user plugs their own agent in
// by implementing this interface.
type Agent interface {
	Name() string
	Decide(view AgentView) Action
}

// Bot is the default role-aware Agent.
type Bot struct {
	name string
	rng  *hashRand
	// seed is kept so table talk can derive its own stream. Chat MUST NOT draw from
	// b.rng: that stream drives move selection, and interleaving a variable number of
	// chat draws into it would change which moves come out — breaking replay.
	seed []byte
	// turn counts Decide calls, so a seat asked twice in one discussion phase does
	// not repeat itself word for word.
	turn int
}

// NewBot builds a deterministic bot for a seat.
func NewBot(name string, seed []byte, seat int) *Bot {
	return &Bot{
		name: name,
		rng:  newHashRand(seed, fmt.Sprintf("bot:%d", seat)),
		seed: append([]byte(nil), seed...),
	}
}

func (b *Bot) Name() string { return b.name }

// Decide returns a legal action for the view's phase. Villagers are never asked at
// night (they are not pending), so the night branch only handles acting roles.
func (b *Bot) Decide(v AgentView) Action {
	switch v.Phase {
	case PhaseNight:
		return b.decideNight(v)
	case PhaseDiscussion:
		// Was one hardcoded sentence for every bot, every round, every match. See
		// chat.go: intent comes from the transcript, wording from the match seed.
		b.turn++
		if act, ok := Speak(b.seed, v, b.turn); ok {
			return act
		}
		// Staying quiet is a legitimate move, and a table where everyone speaks every
		// round is the other way to look mechanical.
		return Action{}
	case PhaseVoting:
		return Action{Kind: ActVote, Target: b.voteTarget(v)}
	}
	return Action{}
}

func (b *Bot) decideNight(v AgentView) Action {
	others := aliveOthers(v) // alive seats != self
	switch v.Role {
	case RoleMafia:
		targets := townTargets(v) // alive, not self, not a fellow mafia
		if len(targets) == 0 {
			targets = others
		}
		return Action{Kind: ActNightKill, Target: pick(b.rng, targets)}
	case RoleDetective:
		return Action{Kind: ActInvestigate, Target: pick(b.rng, uninvestigated(v))}
	case RoleDoctor:
		// The doctor may shield anyone, including itself.
		return Action{Kind: ActProtect, Target: pick(b.rng, aliveAll(v))}
	case RoleSheriff:
		return Action{Kind: ActProfile, Target: pick(b.rng, others)}
	}
	return Action{}
}

// voteTarget concentrates votes so a game makes progress. A Detective votes a
// player it has PROVEN is Mafia; Mafia push the lowest living town seat; everyone
// else piles onto the lowest living seat that isn't themselves.
func (b *Bot) voteTarget(v AgentView) int {
	if v.Role == RoleDetective {
		for _, seat := range knownMafia(v) {
			if v.Alive[seat] {
				return seat
			}
		}
	}
	if v.Role == RoleMafia {
		if t := townTargets(v); len(t) > 0 {
			return t[0]
		}
	}
	// Vote the way you argued.
	//
	// Town used to fall through to "lowest living seat", which made every table
	// predictable (survive by not being seat 1) and — now that the bots actually
	// discuss — incoherent: a seat would spend the round accusing seat 3 and then
	// vote seat 1. A player reading the transcript would watch bots contradict
	// themselves, which reads as broken rather than as bluffing.
	//
	// So town follows the accusation it made, or the loudest one on the table. This
	// also makes the discussion MATTER: talk you can influence changes the vote, which
	// is the whole point of practising against it.
	if t := b.accusationTarget(v); t >= 0 && v.Alive[t] && t != v.Seat {
		return t
	}
	// Nobody argued for anything — a round of pure mourning, which happens often on
	// day one when the only news is the night kill.
	//
	// This used to return the lowest living seat, and that is the whole "survive by
	// not being seat 1" problem the doc comment above already calls out: it is not
	// only predictable, it is decisive. Every silent bot picks the SAME seat, so a
	// quiet round is an automatic unanimous lynch of whoever sits lowest, before that
	// player has said or done anything.
	//
	// A per-seat draw removes the coordination without removing the determinism. Its
	// own hash stream, keyed by seat and day, so it is replay-stable and does not
	// disturb the draw order of the chat or night streams — those are seeded
	// independently, and a shared generator would make one phase's draws move
	// another's.
	others := aliveOthers(v)
	if len(others) == 0 {
		return v.Seat
	}
	return pick(newHashRand(b.seed, fmt.Sprintf("vote:noread:%d:%d", v.Seat, v.Day)), others)
}

// accusationTarget returns the seat this bot argued against this round, falling back
// to whoever the table pressured most. Its own accusation wins: a seat that talks
// itself into a read and then follows someone else's is not how a table behaves.
// Returns -1 when the table argued for nothing, NOT 0.
//
// Seat 0 is a real player, so 0 cannot double as "no read" — with the old sentinel a
// bot that accused seat 0, or a table that pressured it hardest, had its own
// conclusion silently discarded and fell through to the seat-order default instead.
// The one seat that could never be voted on the strength of an argument was the first
// one at the table.
func (b *Bot) accusationTarget(v AgentView) int {
	tally := map[int]int{}
	mine := -1
	for _, e := range v.Public {
		m, ok := e.Payload.(MessagePayload)
		if !ok || m.Target == nil {
			continue
		}
		t := *m.Target
		if t == v.Seat || !v.Alive[t] {
			continue // never vote yourself, or a corpse
		}
		if m.From == v.Seat {
			mine = t
		}
		tally[t]++
	}
	if mine >= 0 {
		return mine
	}
	// Otherwise the seat under the most pressure. Ties resolve to the lowest seat so
	// the choice stays deterministic and replay-stable.
	best, bestN := -1, 0
	for seat, n := range tally {
		if n > bestN || (n == bestN && best >= 0 && seat < best) {
			best, bestN = seat, n
		}
	}
	return best
}

// knownMafia returns seats this agent has personally proven are Mafia, from its own
// private night results (Detective findings).
func knownMafia(v AgentView) []int {
	var out []int
	for _, ev := range v.Private {
		if np, ok := ev.Payload.(NightPayload); ok && np.Finding == "MAFIA" {
			out = append(out, np.Target)
		}
	}
	sort.Ints(out)
	return out
}

// uninvestigated returns living non-self seats this Detective has not yet checked
// (falling back to all living others once everyone has been investigated).
func uninvestigated(v AgentView) []int {
	done := map[int]bool{}
	for _, ev := range v.Private {
		if np, ok := ev.Payload.(NightPayload); ok && np.Target > 0 {
			done[np.Target] = true
		}
	}
	var out []int
	for _, seat := range aliveOthers(v) {
		if !done[seat] {
			out = append(out, seat)
		}
	}
	if len(out) == 0 {
		return aliveOthers(v)
	}
	return out
}

// ── view helpers ───────────────────────────────────────────────────────────

func aliveAll(v AgentView) []int {
	var out []int
	for seat, alive := range v.Alive {
		if alive {
			out = append(out, seat)
		}
	}
	sort.Ints(out)
	return out
}

func aliveOthers(v AgentView) []int {
	var out []int
	for _, seat := range aliveAll(v) {
		if seat != v.Seat {
			out = append(out, seat)
		}
	}
	return out
}

func townTargets(v AgentView) []int {
	ally := map[int]bool{}
	for _, a := range v.Allies {
		ally[a] = true
	}
	var out []int
	for _, seat := range aliveOthers(v) {
		if !ally[seat] {
			out = append(out, seat)
		}
	}
	return out
}

func pick(rng *hashRand, xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	return xs[rng.Intn(len(xs))]
}
