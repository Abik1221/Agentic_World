package mafia

import "encoding/json"

// Version is the Mafia rules engine semver surfaced on match rows.
const Version = "1.1.0"

// Event kinds mirror the spectator SSE contract (Frontend/lib/mafia.ts).
type EventType string

const (
	EvPhase     EventType = "phase"
	EvModerator EventType = "moderator"
	EvNight     EventType = "night"
	EvMessage   EventType = "message"
	EvVote      EventType = "vote"
	EvEliminate EventType = "eliminate"
	EvVictory   EventType = "victory"
)

// Event is one append-only log entry. Seq is gap-free per match.
type Event struct {
	Seq     int       `json:"seq"`
	Type    EventType `json:"type"`
	Payload any       `json:"payload"`
}

type PhasePayload struct {
	Day   int    `json:"day"`
	Phase string `json:"phase"`
	// DurationMs is how long this phase runs. It ships with the phase event so a
	// SPECTATOR can run the same countdown the players see — previously the shot
	// clock existed only on the authenticated agent view, so watchers had no way
	// to know how long night lasts or when debate closes.
	DurationMs int64 `json:"duration_ms"`
}

type ModeratorPayload struct {
	Text string `json:"text"`
}

type NightPayload struct {
	Actor   string `json:"actor"`
	Seat    int    `json:"seat"`              // the acting seat; used to redact this secret to its owner only
	Target  int    `json:"target,omitempty"`  // the seat acted upon (e.g. investigated)
	Finding string `json:"finding,omitempty"` // structured result for the actor: "MAFIA" | "TOWN"
	Text    string `json:"text"`
	Secret  string `json:"secret,omitempty"`
}

type MessagePayload struct {
	From   int    `json:"from"`
	Tone   string `json:"tone"`
	Text   string `json:"text"`
	Target *int   `json:"target,omitempty"`
}

type VotePayload struct {
	From   int `json:"from"`
	Target int `json:"target"`
}

type EliminatePayload struct {
	Target int    `json:"target"`
	Cause  string `json:"cause"`
	Role   string `json:"role,omitempty"` // revealed only when RevealRoleOnDeath is on
}

type VictoryPayload struct {
	Team string `json:"team"`
	Text string `json:"text"`
}

// DecodePayload restores a persisted JSON payload to its concrete type for the
// given event kind. Persistence stores payloads as opaque JSON; consumers that
// inspect payload fields — notably BuildView's per-seat redaction and the bots'
// reading of their own private night results — rely on the typed value rather
// than a generic map. Decoding into VALUE types keeps the `ev.Payload.(XPayload)`
// assertions used across the engine working after a database round-trip.
func DecodePayload(t EventType, raw []byte) (any, error) {
	switch t {
	case EvPhase:
		var p PhasePayload
		return p, json.Unmarshal(raw, &p)
	case EvModerator:
		var p ModeratorPayload
		return p, json.Unmarshal(raw, &p)
	case EvNight:
		var p NightPayload
		return p, json.Unmarshal(raw, &p)
	case EvMessage:
		var p MessagePayload
		return p, json.Unmarshal(raw, &p)
	case EvVote:
		var p VotePayload
		return p, json.Unmarshal(raw, &p)
	case EvEliminate:
		var p EliminatePayload
		return p, json.Unmarshal(raw, &p)
	case EvVictory:
		var p VictoryPayload
		return p, json.Unmarshal(raw, &p)
	default:
		var p any
		return p, json.Unmarshal(raw, &p)
	}
}
