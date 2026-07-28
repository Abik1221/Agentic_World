package mafia

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
