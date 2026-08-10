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

	// Public is the shared transcript (moderator lines, messages, votes,
	// eliminations, phase changes, victory) every agent may see.
	Public []Event `json:"public"`
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

func cloneAliveMap(m map[int]bool) map[int]bool {
	out := make(map[int]bool, len(m))
	for k, val := range m {
		out[k] = val
	}
	return out
}
