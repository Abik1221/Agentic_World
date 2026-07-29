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
	others := aliveOthers(v)
	if len(others) > 0 {
		return others[0]
	}
	return v.Seat
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
