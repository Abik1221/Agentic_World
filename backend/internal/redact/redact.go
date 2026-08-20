// Package redact holds one rule: what a match event may reveal before the match
// is over.
//
// # Why it is its own package
//
// The rule was implemented once, in the mafia hub, and every other path that
// serves match events simply did not ask. Two of them shipped the same leak:
//
//	GET /v1/match/{id}/watch   — the public SSE stream (spectator hub)
//	GET /v1/match/{id}/replay  — the public replay document (match service)
//
// Both serve EVERY match id, both are unauthenticated, and both loaded the raw
// event log from the store. Mafia writes its night actions to that same log, so
// on a LIVE match either endpoint returned:
//
//	{"actor":"Mafia","seat":9,"secret":"Target → seat 4"}
//
// — every role and every night target, to anyone who asked. `pyyol watch` and
// `pyyol replay` use exactly these two endpoints.
//
// Putting the rule in a package that both import is the point. Two copies of a
// security predicate drift, and the failure mode is silent: the copy nobody
// updated keeps answering "safe".
//
// The platform's stated invariant is "hidden information never leaves the
// server", and the engine's own doctrine (engine/mafia/redact.go) says night
// events are the only entries carrying secrets. This package enforces that at
// the transport boundary, which is the layer that was actually leaking.
package redact

import "encoding/json"

// EvNight is Mafia's secret-bearing event kind. Compared as a string because the
// paths that need this rule carry events typed for a different engine — the
// generic hub is Goofspiel-typed and still receives Mafia's rows from the store.
const EvNight = "night"

// SafeForLive reports whether an event may be shown before a match has finished.
//
// Two rules, deliberately belt-and-braces:
//
//	kind    — night events are dropped outright, mirroring engine/mafia.PublicEvent.
//	payload — anything carrying a "secret" field is dropped whatever its type is,
//	          so a future secret-bearing event cannot leak through merely because
//	          nobody remembered to add its name to a list.
func SafeForLive(eventType string, payload any) bool {
	if eventType == EvNight {
		return false
	}
	return !HasSecret(payload)
}

// HasSecret reports whether a payload carries a "secret" field — for a typed
// struct, or for the map a payload becomes once it has been through the store.
func HasSecret(payload any) bool {
	if payload == nil {
		return false
	}
	if m, ok := payload.(map[string]any); ok {
		_, found := m["secret"]
		return found
	}
	b, err := json.Marshal(payload)
	if err != nil {
		// Fails CLOSED. On a boundary whose job is to withhold secrets, "I could
		// not tell what this is" must mean "do not send it".
		return true
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return false // not an object, so it has no fields to leak
	}
	_, found := m["secret"]
	return found
}
