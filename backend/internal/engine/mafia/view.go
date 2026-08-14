package mafia

import "sort"

// view.go builds the redacted, per-seat picture an agent is allowed to see. Mafia
// is a hidden-role game: an agent must never see other players' roles or the
// secret outcomes of other players' night actions. BuildView enforces that — it
// is what makes "let a user test their agent" fair and what a real client would
// receive over the wire.

// AgentView is everything seat `Seat` legitimately knows right now.
type AgentView struct {
	Seat   int          `json:"seat"`
	Role   string       `json:"role"`
	Team   string       `json:"team"`
	Day    int          `json:"day"`
	Phase  string       `json:"phase"`
	Alive  map[int]bool `json:"alive"`
	Allies []int        `json:"allies,omitempty"` // fellow Mafia seats (Mafia agents only)
	Legal  []string     `json:"legal"`            // action kinds valid for this seat now
	// CannotProtect is the seat this DOCTOR shielded last night and therefore may not shield
	// again tonight. -1 when there is no such seat (the first night, or after a night off).
	//
	// Published rather than left to be discovered by rejection: the rule is real, and an agent
	// that has to learn it by having a move refused wastes a decision and a model call to find
	// out something the engine already knows. Doctors only — nobody else's constraint is
	// anyone else's business.
	CannotProtect int `json:"cannot_protect,omitempty"`

	// Public is the shared transcript (moderator lines, messages, votes,
	// eliminations, phase changes, victory) every agent may see.
	//
	// WINDOWED to the most recent MaxPublicWindow entries — see the note where it is built.
	// Everything older is summarised in Digest, and every event was already delivered to the
	// agent in real time as it happened, so this is de-duplication rather than redaction.
	Public []Event `json:"public"`
	// Digest states, in counts and seat numbers only, what happened before the window.
	// Nil when the whole transcript fits. Never contains prose: a summary of what an agent
	// said is not what it said, and this game is played through speech.
	Digest *PublicDigest `json:"digest,omitempty"`
	// Private is this seat's own night results (e.g., an investigation outcome).
	// Other seats' night secrets are never included.
	Private []Event `json:"private,omitempty"`
}

// isPublic reports whether an event type is part of the shared transcript.
//
// EvSilent is public, and that is the point of it: an agent asked to vote must be able
// to see which seats went dark, exactly as it sees who spoke and who voted. The engine
// only ever emits it for the discussion and voting phases — night silence would reveal
// role membership, so actNight deliberately emits none.
func isPublic(t EventType) bool {
	switch t {
	case EvPhase, EvModerator, EvMessage, EvVote, EvEliminate, EvVictory, EvSilent:
		return true
	}
	return false
}

// BuildView produces the redacted view for `seat` from the full match state and
// event log. The log is the engine's append-only stream; this function decides
// what slice of it the seat is entitled to.
func BuildView(s State, seat int, log []Event) AgentView {
	role := s.Roles[seat]
	v := AgentView{
		Seat:  seat,
		Role:  role,
		Team:  TeamOf(role),
		Day:   s.Day,
		Phase: s.Phase,
		Alive: cloneAliveMap(s.Alive),
		Legal: LegalActions(s, seat),
	}

	if role == RoleDoctor {
		if last, ok := s.LastProtect[seat]; ok {
			v.CannotProtect = last
		} else {
			v.CannotProtect = -1
		}
	}

	if role == RoleMafia {
		var allies []int
		for other, r := range s.Roles {
			if other != seat && r == RoleMafia {
				allies = append(allies, other)
			}
		}
		sort.Ints(allies)
		v.Allies = allies
	}

	// COMPLETE, deliberately. BuildView feeds three consumers and only one of them wants a
	// smaller payload:
	//
	//   - dispatchPublicEvents, the real-time push stream — windowing here would SILENTLY DROP
	//     events an agent never received, breaking the guarantee that nobody misses anything
	//   - the game-end record, documented as the complete match transcript
	//   - the per-turn payload sent to the model, which is the only one paying per token
	//
	// So the trimming lives at the payload boundary (WindowPublic), not here. A view that is
	// authoritative for the log must stay whole.
	for _, ev := range log {
		switch {
		case isPublic(ev.Type):
			v.Public = append(v.Public, ev)
		case ev.Type == EvNight:
			if np, ok := ev.Payload.(NightPayload); ok && np.Seat == seat {
				v.Private = append(v.Private, ev) // only my own night result
			}
		}
	}
	return v
}

// WindowPublic trims a view's transcript for the PER-TURN PAYLOAD only.
//
// Measured on a real finished match in the lab: 550 public events. At 50-200 tokens each
// that is 27,000-110,000 tokens in one prompt, against a Groq free tier of 6,000 TOKENS PER
// MINUTE — one turn exceeding a minute's entire budget by an order of magnitude.
//
// It is also redundant: every event was already pushed to the agent in real time as it
// happened, so re-sending the archive each turn pays for the same information again, per
// turn, forever. This removes DUPLICATION, never information.
//
// Call this where the view is serialised to the model. Never inside BuildView — see the note
// there for the two consumers that must keep the whole log.
func WindowPublic(public []Event) ([]Event, *PublicDigest) {
	if len(public) <= MaxPublicWindow {
		return public, nil
	}
	cut := len(public) - MaxPublicWindow
	return append([]Event(nil), public[cut:]...), digestOf(public[:cut])
}

// MaxPublicWindow is how many recent public events travel in a per-turn payload.
//
// Sized to cover a full day of a 12-seat table — roughly one phase banner, twelve messages,
// twelve votes and an elimination, with room to spare — so the current conversation is always
// present in full. Older days arrive as a Digest, and the complete log stays available from
// the replay endpoint for anything that wants it.
const MaxPublicWindow = 60

// PublicDigest states what happened before the window in facts, never in prose.
type PublicDigest struct {
	// Events is how many public entries the window left out, so an agent can tell that
	// history exists rather than inferring the match began where the window starts.
	Events int `json:"events"`
	// Messages, Votes and Eliminations are counts of what was dropped.
	Messages     int `json:"messages"`
	Votes        int `json:"votes"`
	Eliminations int `json:"eliminations"`
	// Eliminated names the seats removed before the window, in order. The single most
	// load-bearing fact in the omitted history: who is gone. Alive already carries the
	// current roster, but the ORDER of removals is how a town reconstructs the night.
	Eliminated []int `json:"eliminated,omitempty"`
}

// digestOf derives the summary. Counts and seat numbers only — no text is copied or
// rewritten, so nothing here can misrepresent what a player actually said.
func digestOf(dropped []Event) *PublicDigest {
	if len(dropped) == 0 {
		return nil
	}
	d := &PublicDigest{Events: len(dropped)}
	for _, ev := range dropped {
		switch ev.Type {
		case EvMessage:
			d.Messages++
		case EvVote:
			d.Votes++
		case EvEliminate:
			d.Eliminations++
			if p, ok := ev.Payload.(EliminatePayload); ok {
				d.Eliminated = append(d.Eliminated, p.Target)
			}
		}
	}
	return d
}

func cloneAliveMap(m map[int]bool) map[int]bool {
	out := make(map[int]bool, len(m))
	for k, val := range m {
		out[k] = val
	}
	return out
}
