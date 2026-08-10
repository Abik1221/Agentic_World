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
// That token proves "the platform issued this turn, and a real LLM call came back
// carrying its token". On its own it does NOT prove the model's answer became the move:
// an agent could call the model, ignore the response, and submit a scripted move with
// every proof valid.
//
// COMPLETION BINDING closes that. The gateway is the one party that sees both the model's
// output and, later, the submitted move, so it extracts the move from the completion's
// structured tool call (internal/movebind) and the platform mints a second artefact:
//
//	receipt = HMAC-SHA256(secret, agent | match | round | completion_hash | extracted_move)
//
// At match time the submitted move must equal the extracted one. The remaining gap is
// prompt-side: an agent can engineer a prompt toward an answer it already wanted. That is
// strategy on this platform rather than fraud, and is deliberately not chased — inferring
// intent from prompt content would be both evadable and unfair to legitimate agents.
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

// --- Completion binding -------------------------------------------------------------
//
// The token above is the agent's CREDENTIAL for a turn: the platform mints it, the agent
// presents it, and it proves a model call belongs to this decision. The receipt below is
// the opposite direction — the platform's RECEIPT for what the model answered, minted only
// after the gateway has seen the completion, and covering the answer itself.
//
// Two different messages under one secret, so the domain tag differs and a token can never
// be presented as a receipt or the reverse. Without distinct tags a v1 token for
// (agent, match, round) would be a valid receipt for the empty completion and empty move,
// which is exactly the substitution this is meant to stop.

// decisionMessage is the byte string a receipt covers.
//
// Field separator matches the v1 message for the same reason: it cannot occur in an id, a
// hex digest or a canonical move, so no two distinct decisions can render to one message.
func decisionMessage(agentID, matchID string, round int, completionHash, move string) []byte {
	return []byte(fmt.Sprintf("pyyol-decision-v1|%s|%s|%d|%s|%s",
		agentID, matchID, round, completionHash, move))
}

// MintDecision issues the receipt binding one decision to the completion that produced it.
//
// completionHash pins the exact response bytes the gateway observed; move is the canonical
// form extracted from that response (internal/movebind). Together they are the difference
// between "a model was called for this turn" and "the model answered THIS".
//
// Returns "" when disabled, exactly like Mint: a deployment with no secret has no proof to
// offer, which is safer than a placeholder something could mistake for one.
func (s *Signer) MintDecision(agentID, matchID string, round int, completionHash, move string) string {
	if !s.Enabled() {
		return ""
	}
	m := hmac.New(sha256.New, s.secret)
	m.Write(decisionMessage(agentID, matchID, round, completionHash, move))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// VerifyDecision reports whether receipt is one this platform minted for exactly this
// (agent, match, round, completion, move). Constant-time.
//
// This is what makes the agent-carried path safe: the agent hands back the move and the
// hash, and cannot alter either without invalidating a MAC it cannot recompute.
func (s *Signer) VerifyDecision(agentID, matchID string, round int, completionHash, move, receipt string) bool {
	if !s.Enabled() || receipt == "" {
		return false
	}
	want := s.MintDecision(agentID, matchID, round, completionHash, move)
	return hmac.Equal([]byte(want), []byte(receipt))
}
