package mafia

// redact.go defines what the server may reveal to a LIVE SPECTATOR (or any
// public consumer of the watch/replay stream) while a match is still running.
//
// The engine keeps a full, append-only event log server-side — including night
// targets, investigation findings and the acting seats — because the post-match
// replay and the tamper-evident replay hash depend on it. But that hidden
// information must never leave the server until the match ends, per the spec:
//
//	"Hidden information never leaves the server."
//	"Replay contains: Role Assignment (hidden until match end) ... Night Actions".
//
// Night events (EvNight) are the only entries that carry secrets — the mafia's
// kill target, the detective's finding, the doctor's protect, the sheriff's
// profile, and (via Seat/Text) which seat holds which role. Public events
// (phase, moderator, message, vote, eliminate, victory) carry only information
// every player already sees. RedactLog therefore drops night events entirely
// from the live view; their public consequences still surface through the
// morning moderator/eliminate events.

// PublicEvent reports whether an event may be shown to a live spectator.
func PublicEvent(ev Event) bool {
	return ev.Type != EvNight
}

// RedactLog returns the spectator-safe subset of an event log: every public
// event, in order, with all night secrets removed. Sequence numbers are
// preserved (the stream stays monotonic for Last-Event-ID resume; gaps where
// night events were dropped are expected and harmless).
func RedactLog(events []Event) []Event {
	out := make([]Event, 0, len(events))
	for _, ev := range events {
		if PublicEvent(ev) {
			out = append(out, ev)
		}
	}
	return out
}
