// Package movesig provides per-move authenticity: an agent signs each move with
// its Ed25519 private key, and anyone can verify the signature against the agent's
// registered public key. This proves the AGENT authored the move — not just that
// the game math is consistent — closing the "the operator could forge/alter a
// move" gap (see docs/game-engine-audit.md). It is a pure leaf: stdlib crypto only,
// no I/O, so both the match worker (verify on submit) and the replay verifier
// (re-check after the match) use the identical code.
package movesig

import (
	"crypto/ed25519"
	"encoding/base64"
	"strconv"
)

// domain separates these signatures from any other use of the agent's key and
// pins the message format; bump the version on any format change.
const domain = "goofspiel-move-v1"

// Message is the canonical bytes an agent signs for one move. Binding (match,
// round, seat, card) means a signature is valid for exactly one slot and one card
// — it cannot be replayed onto a different round, match, seat, or value.
func Message(matchPublicID string, round, seat, card int) []byte {
	// domain \n match \n round \n seat \n card  (newline-delimited, unambiguous).
	b := make([]byte, 0, 64)
	b = append(b, domain...)
	b = append(b, '\n')
	b = append(b, matchPublicID...)
	b = append(b, '\n')
	b = strconv.AppendInt(b, int64(round), 10)
	b = append(b, '\n')
	b = strconv.AppendInt(b, int64(seat), 10)
	b = append(b, '\n')
	b = strconv.AppendInt(b, int64(card), 10)
	return b
}

// Move-domain constants for the non-card games. Goofspiel keeps its implicit
// "goofspiel-move-v1" domain baked into Message above; the richer games bind an
// action STRING (verb + params) instead of a single card integer.
const (
	DomainMafia    = "mafia-move-v1"
	DomainMonopoly = "monopoly-move-v1"
)

// ActionMessage is the canonical bytes an agent signs for one non-card move. It
// binds (domain, match, seq, seat, action) so a signature is valid for exactly
// one slot (seq+seat) and one action string — it cannot be replayed onto a
// different match, slot, seat, or action. `seq` is a per-move ordinal the agent
// and server agree on (Mafia: day; Monopoly: move sequence); `action` is a
// deterministic canonical encoding of the move (e.g. Mafia "night|kill|3",
// Monopoly "buy" / "bid|150") that both signer and verifier compute identically.
func ActionMessage(domain, matchPublicID string, seq, seat int, action string) []byte {
	b := make([]byte, 0, 96)
	b = append(b, domain...)
	b = append(b, '\n')
	b = append(b, matchPublicID...)
	b = append(b, '\n')
	b = strconv.AppendInt(b, int64(seq), 10)
	b = append(b, '\n')
	b = strconv.AppendInt(b, int64(seat), 10)
	b = append(b, '\n')
	b = append(b, action...)
	return b
}

// VerifyAction reports whether sigB64 is a valid signature by pubKeyB64 over the
// canonical ActionMessage for (domain, match, seq, seat, action). Any decode/size
// error → false.
func VerifyAction(domain, pubKeyB64, matchPublicID string, seq, seat int, action, sigB64 string) bool {
	pub, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), ActionMessage(domain, matchPublicID, seq, seat, action), sig)
}

// ValidPublicKey reports whether b64 decodes to a well-formed Ed25519 public key.
func ValidPublicKey(b64 string) bool {
	raw, err := base64.StdEncoding.DecodeString(b64)
	return err == nil && len(raw) == ed25519.PublicKeySize
}

// Verify reports whether sigB64 is a valid signature by pubKeyB64 over the
// canonical message for (match, round, seat, card). Any decode/size error → false.
func Verify(pubKeyB64, matchPublicID string, round, seat, card int, sigB64 string) bool {
	pub, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), Message(matchPublicID, round, seat, card), sig)
}
