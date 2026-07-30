// Package turnproof binds a server-observed LLM call to the exact decision it was
// made for.
//
// THE PROBLEM. Pyyol is an arena for AI agents, and in ranked play that claim is
// backed by real money. But nothing forced a ranked agent to be AI-backed at all: a
// hand-written deterministic script could certify, queue, and win stakes from agents
// that were genuinely paying for inference. The LLM Gateway already observes provider,
// model, tokens and cost server-side — unfakeable — but routing through it was
// optional and earned only a badge.
//
// Making the gateway mandatory is not enough on its own, because the old binding was
// self-declared: the gateway read the match id from X-Pyyol-Match, a header the AGENT
// sets. An agent could make one cheap call and label it with any match, or label a
// hundred decisions with one call. That proves an LLM call happened somewhere. It does
// not prove THIS decision was made by one.
//
// THE APPROACH. The platform issues a token with each turn view, derived from a secret
// only the server holds:
//
//	token = HMAC-SHA256(secret, agent | match | round)
//
// The agent cannot compute a token for a turn it was not given, and a token it saved
// from round 1 verifies as round 1 — so replaying it does not make round 5 look
// LLM-backed. Verification is a recomputation, so the gateway needs no per-turn
// storage on the hot path and nothing has to be cleaned up.
//
// This deliberately proves only "the platform issued this turn, and a real LLM call
// came back carrying its token". It does not attempt to prove the model's output was
// USED for the move — that is unknowable from outside the agent, and any attempt to
// infer it from move content would be both evadable and unfair to legitimate agents.
package turnproof

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// Signer mints and verifies turn tokens. The zero value is disabled: Mint returns ""
// and Verify returns false, so a deployment with no secret configured cannot
// accidentally accept unbound calls as proof — it simply has no proof to offer.
type Signer struct{ secret []byte }

// New builds a Signer. An empty secret yields a disabled Signer rather than an error,
// so local and sandbox deployments run without ceremony.
func New(secret string) *Signer {
	if secret == "" {
		return &Signer{}
	}
	return &Signer{secret: []byte(secret)}
}

// Enabled reports whether a secret is configured.
func (s *Signer) Enabled() bool { return s != nil && len(s.secret) > 0 }

// message is the exact byte string covered by the MAC. Fields are separated by a
// character that cannot appear in an id, so ("ab","c") and ("a","bc") cannot collide
// into the same message — without that, a token for one match could verify for another.
func message(agentID, matchID string, round int) []byte {
	return []byte(fmt.Sprintf("pyyol-turn-v1|%s|%s|%d", agentID, matchID, round))
}

// Mint issues the token for one turn. Returns "" when disabled.
func (s *Signer) Mint(agentID, matchID string, round int) string {
	if !s.Enabled() {
		return ""
	}
	m := hmac.New(sha256.New, s.secret)
	m.Write(message(agentID, matchID, round))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Verify reports whether token is the one this platform issued for exactly this
// (agent, match, round). Constant-time, so a caller cannot learn a valid token by
// timing its rejections.
func (s *Signer) Verify(agentID, matchID string, round int, token string) bool {
	if !s.Enabled() || token == "" {
		return false
	}
	want := s.Mint(agentID, matchID, round)
	return hmac.Equal([]byte(want), []byte(token))
}
