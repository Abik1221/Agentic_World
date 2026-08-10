package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/movebind"
)

// bind.go — drive a decision through the Pyyol LLM Gateway as a STRUCTURED TOOL CALL, so the
// completion-binding path is exercised against the real server rather than against a mock.
//
// # Why the lab needs this
//
// Completion binding spans four components: the SDK asks the model for a tool call, the gateway
// extracts the move from the completion, the store records it against the turn, and the game
// service compares it to the submitted move. Every one of those has unit tests. None of those
// tests can tell you whether the four agree, because each mocks the other three — and the
// history of this codebase is that a control passed its tests and sat where the traffic did not
// go. Only a real match, against the real server, over the real HTTP surface, answers that.
//
// # The two things it proves
//
//   - HONEST PLAY IS BOUND. The agent asks the gateway for a move, gets card N back, submits
//     card N, and the turn is accepted with a bound decision recorded.
//   - A SUBSTITUTED MOVE IS REJECTED. With -substitute-at R the agent binds card N and then
//     deliberately submits a DIFFERENT card. The platform must refuse it. Without this half the
//     first half proves only that nothing is broken, not that anything is enforced.
//
// The provider is a local stand-in that answers with a play_card tool call; the card comes back
// from a header the agent sets, so the lab controls what "the model answered" and can therefore
// construct a disagreement on purpose.

// BindThroughGateway enables the gateway path for every decision. Off by default: the ordinary
// lab run measures the platform's own shot clock and forfeit behaviour, and adding a proxy hop
// to that would change what it measures.
var BindThroughGateway = false

// BindGatewayBase is the gateway's base URL as seen from the agent process.
var BindGatewayBase = ""

// BindProvider names the gateway upstream to route through, and BindKey is the developer's
// own credential for it. Empty ⇒ the local stand-in provider on the anthropic path, which is
// what every other lab run uses.
//
// Exists so the lab can drive a REAL model end to end — the stand-in proves the plumbing, but
// only a real provider proves that the usage block we normalize, the cost we record and the
// tool call we bind are the ones that provider actually sends.
var (
	BindProvider = ""
	BindKey      = ""
	BindModel    = ""
)

// BindStream routes the decision as a STREAMED completion. Worth a separate run: streamed
// responses were captured nowhere before completion binding, so the reassembly path is newer
// and less exercised than the single-object one.
var BindStream = false

// SubstituteAtRound is the round from which the agent submits a card OTHER than the one it
// bound. 0 disables it. This is the negative half of the proof, and the run is only meaningful
// if the platform actually refuses the move.
var SubstituteAtRound = 0

// --- Honest-but-unbound behaviour, for measuring the ranked threshold ---------------------
//
// A threshold on "share of decisions proven LLM-backed" can only be chosen from the distribution
// HONEST agents produce. A harness that binds every round by construction scores 100% and tells
// you nothing: it measures the rig, not the population.
//
// These reproduce the three ways a genuinely honest agent legitimately ends a round unbound.
// Every one of them still PLAYS — that is the whole point. An agent whose provider hiccuped is
// not cheating, and a threshold that voids it is a bug in the threshold.

// BindFailPct is the percentage of turns whose model call fails outright (a provider 5xx, a
// timeout, a rate limit). The agent falls back to its own strategy and plays on, unbound.
var BindFailPct = 0

// BindBatchRounds makes ONE model call cover this many rounds — the agent plans ahead and then
// plays several moves from that single decision. Legitimate and common for cost control, and it
// structurally produces fewer bindings than rounds.
var BindBatchRounds = 0

// bindLuck decides, deterministically, whether this specific turn's call fails.
//
// Deterministic on (match, round, seat) rather than random so a run is reproducible and two
// seats do not fail in lockstep. Math/rand would make the measured distribution unrepeatable,
// which for a number that decides whether real matches get voided is not good enough.
func bindLuck(matchID string, round, seat int) int {
	h := 2166136261
	for _, c := range []byte(matchID) {
		h = (h ^ int(c)) * 16777619
	}
	h = (h ^ (round * 2654435761)) * 16777619
	h = (h ^ (seat * 40503)) * 16777619
	if h < 0 {
		h = -h
	}
	return h % 100
}

// bindThisTurn reports whether the agent should route this turn through the gateway at all, and
// why not when it should not. The reason is logged so a run's coverage can be explained rather
// than merely observed.
//
// Batching no longer appears here. It used to: a batched round made no call and was recorded
// UNBOUND, which is precisely what put the honest floor at ~33% and made the share rule punish
// the cost optimisation Phase 4 rewards. A batched round now plays from a span the anchor call
// already bound, so it is covered rather than skipped.
func bindThisTurn(matchID string, round, seat int) (ok bool, why string) {
	if BindFailPct > 0 && bindLuck(matchID, round, seat) < BindFailPct {
		return false, "provider call failed (simulated 5xx/timeout) — playing unbound"
	}
	return true, ""
}

// isSpanAnchor reports whether this round is where a batching agent makes its one call.
func isSpanAnchor(round int) bool {
	return BindBatchRounds > 1 && (round-1)%BindBatchRounds == 0
}

// planStep is one round of a batched decision.
type planStep struct {
	Round int
	Card  int
}

// buildSpan decides the next BindBatchRounds moves in one go.
//
// The anchor round plays what the strategy chose; the rounds after it take DISTINCT cards from
// the remaining hand, which is what keeps them legal — Goofspiel removes each card as it is
// played, so committing to cards the agent still holds and playing them in order cannot produce
// an illegal move. Ascending order rather than a strategy, because the point of the lab is to
// measure coverage, and a plan that occasionally became illegal would measure rejections
// instead.
func buildSpan(round, card int, legal []int, n int) []planStep {
	span := []planStep{{Round: round, Card: card}}
	rest := make([]int, 0, len(legal))
	for _, c := range legal {
		if c != card {
			rest = append(rest, c)
		}
	}
	sort.Ints(rest)
	for i := 0; i < len(rest) && len(span) < n; i++ {
		span = append(span, planStep{Round: round + len(span), Card: rest[i]})
	}
	return span
}

// spanHeader renders a plan for the stand-in provider: "4:7,5:2,6:9".
func spanHeader(span []planStep) string {
	parts := make([]string, 0, len(span))
	for _, s := range span {
		parts = append(parts, fmt.Sprintf("%d:%d", s.Round, s.Card))
	}
	return strings.Join(parts, ",")
}

// bindResult is what one gateway round trip established.
type bindResult struct {
	// Card is the move the MODEL produced, as read back from its tool call.
	Card int
	// Bound reports whether the platform will have a binding for this turn. False means the
	// call did not carry a usable proof or tool call, in which case enforcement is inert and
	// the run proves nothing — so the caller logs it loudly rather than treating it as success.
	Bound bool
	// Span is every round this ONE completion decided, as the gateway will have bound them.
	// Length 1 for an agent that does not batch.
	Span []planStep
}

// decideThroughGateway asks the model, through the gateway, for this turn's move.
//
// wantCard is the card the stand-in provider is told to answer with, so the lab stays
// deterministic. In a real agent the model would choose; here the STRATEGY still chooses (the
// lab's own persona logic), and the provider is simply made to say what the strategy decided.
// That keeps the match's play identical to a non-bound run, which is what makes the two
// comparable.
func (a *labAgent) decideThroughGateway(matchID string, round, wantCard int, span []planStep, proof string, legal []int, prize int) (bindResult, error) {
	if BindGatewayBase == "" {
		return bindResult{}, fmt.Errorf("gateway base URL not set")
	}
	if proof == "" {
		// A turn with no proof cannot be bound, and a run that quietly continued would report
		// success while measuring nothing. The usual cause is TURN_PROOF_SECRET unset on the
		// server, which is exactly the misconfiguration this must not hide.
		return bindResult{}, fmt.Errorf(
			"the turn view carried no turn_proof, so this decision CANNOT be bound " +
				"(is TURN_PROOF_SECRET set on the server?)")
	}

	// A real Anthropic Messages request, including the move tool and a tool_choice that
	// REQUIRES it. The gateway does not care about the request beyond the model name — it
	// reads the RESPONSE — but sending the real shape is the point: it is what an SDK agent
	// sends, so the run exercises the same bytes.
	reqBody := map[string]any{
		"model":      "claude-opus-4",
		"max_tokens": 256,
		"stream":     BindStream,
		"tools": []map[string]any{{
			"name":        movebind.ToolGoofspiel,
			"description": "Play one card from your hand for this round.",
			"input_schema": moveToolSchema(len(span)),
		}},
		"tool_choice": map[string]any{"type": "tool", "name": movebind.ToolGoofspiel},
		"messages": []map[string]any{{
			"role":    "user",
			"content": fmt.Sprintf("Round %d. Choose a card and report it with the tool.", round),
		}},
	}
	// OPENAI WIRE for anything that is not Anthropic. Groq, and every other OpenAI-compatible
	// provider, nests the tool under `function` and names the forcing field differently — send
	// the Anthropic shape and it is a 400, not a silent mis-parse.
	if BindProvider != "" && BindProvider != "anthropic" {
		reqBody = map[string]any{
			"model":      BindModel,
			"max_tokens": 256,
			"stream":     BindStream,
			"tools": []map[string]any{{
				"type": "function",
				"function": map[string]any{
					"name":        movebind.ToolGoofspiel,
					"description": "Play one card from your hand for this round.",
					"parameters":  moveToolSchema(len(span)),
				},
			}},
			"tool_choice": map[string]any{
				"type":     "function",
				"function": map[string]any{"name": movebind.ToolGoofspiel},
			},
			"messages": []map[string]any{{
				"role": "user",
				"content": fmt.Sprintf(
					"Goofspiel round %d. Your legal cards are %v. The prize is worth %d. "+
						"Call play_card with exactly one card from that list.",
					round, legal, prize),
			}},
		}
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return bindResult{}, err
	}

	// The gateway routes /v1/gw/{provider}/*, so the path after the provider is the
	// provider's OWN path — Anthropic's /v1/messages, OpenAI-wire /v1/chat/completions.
	route := "/v1/gw/anthropic/v1/messages"
	if BindProvider != "" && BindProvider != "anthropic" {
		route = "/v1/gw/" + BindProvider + "/v1/chat/completions"
	}
	req, err := http.NewRequest(http.MethodPost, BindGatewayBase+route, bytes.NewReader(raw))
	if err != nil {
		return bindResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	// The developer's own provider credential, passed through untouched. A placeholder here
	// because the upstream is a stand-in; the gateway must not care either way.
	if BindKey != "" {
		// The developer's OWN credential, passed through. This is the whole trust model: they
		// cannot claim a model they are not billed for.
		req.Header.Set("Authorization", "Bearer "+BindKey)
	} else {
		req.Header.Set("x-api-key", "lab-provider-key")
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	// Pyyol identity and the per-turn proof. The proof is what makes the match/turn headers
	// trustworthy — without it the gateway records the call but binds nothing.
	req.Header.Set("X-Pyyol-Key", a.AgentKey)
	req.Header.Set("X-Pyyol-Match", matchID)
	req.Header.Set("X-Pyyol-Turn", fmt.Sprint(round))
	req.Header.Set("X-Pyyol-Proof", proof)
	// Tells the stand-in provider which card to answer with. An ordinary header, so it reaches
	// the upstream: the gateway strips only x-pyyol-* and hop-by-hop.
	req.Header.Set("X-Lab-Card", fmt.Sprint(wantCard))
	// A BATCHED decision: one call that plans several rounds. The stand-in provider answers
	// with a plan tool call, and the gateway binds every round in it from that one completion.
	if len(span) > 1 {
		req.Header.Set("X-Lab-Plan", spanHeader(span))
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return bindResult{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return bindResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return bindResult{}, fmt.Errorf("gateway returned %d: %s", resp.StatusCode,
			truncate(string(body), 300))
	}

	// Read the move back the SAME way the gateway does. If these two ever disagree the run
	// fails here rather than as a mysterious rejection three lines later.
	var tc movebind.ToolCall
	var ok bool
	if BindStream {
		tc, ok = movebind.ExtractStream(body, movebind.ToolGoofspiel)
	} else {
		tc, ok = movebind.Extract(body, movebind.ToolGoofspiel)
	}
	if !ok {
		return bindResult{}, fmt.Errorf(
			"the completion carried no play_card tool call, so nothing was bound: %s",
			truncate(string(body), 300))
	}
	// Reduce the SAME way the gateway will, including the range form. If the lab and the
	// gateway ever disagree the run fails here rather than as a mysterious rejection later.
	covered, ok := movebind.CanonPlan(movebind.GameGoofspiel, tc, round)
	if !ok || len(covered) == 0 {
		return bindResult{}, fmt.Errorf("the tool call did not reduce to a move: %+v", tc)
	}
	if len(span) > 1 && len(covered) != len(span) {
		// The whole point of a batched run is that the span is bound. Silently covering fewer
		// rounds than were planned would understate coverage and look like the metric is still
		// broken, so it fails loudly instead.
		return bindResult{}, fmt.Errorf(
			"planned %d rounds but the completion bound %d: %+v", len(span), len(covered), covered)
	}
	out := bindResult{Bound: true}
	for _, rm := range covered {
		card, cok := cardOf(rm.Move)
		if !cok {
			return bindResult{}, fmt.Errorf("bound move %q is not a card", rm.Move)
		}
		out.Span = append(out.Span, planStep{Round: rm.Round, Card: card})
		if rm.Round == round {
			out.Card = card
		}
	}
	if out.Card == 0 {
		return bindResult{}, fmt.Errorf("the completion bound %d rounds but not round %d itself: %+v",
			len(covered), round, covered)
	}
	return out, nil
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), n == float64(int(n))
	case int:
		return n, true
	}
	return 0, false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// substitutedCard returns a card DIFFERENT from bound, drawn from the legal set.
//
// Picking from the legal set matters: an illegal card would be refused by the engine for the
// wrong reason, and the run would "pass" without the binding check ever being consulted. The
// substitution has to be a move that would otherwise be perfectly acceptable.
func substitutedCard(bound int, legal []int) (int, bool) {
	for _, c := range legal {
		if c != bound {
			return c, true
		}
	}
	return 0, false
}

// moveToolSchema is the tool the lab asks the model for: the plain one-card form, or the plan
// form when this call is deciding several rounds.
//
// Mirrors what the SDKs build (pyyol.movetools.move_tool / moveTool), because the lab exists to
// exercise what a developer's agent actually sends. A schema only the lab uses would test the
// gateway against a shape no real agent produces.
func moveToolSchema(spanLen int) map[string]any {
	card := map[string]any{"type": "integer"}
	if spanLen <= 1 {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{"card": card},
			"required":   []string{"card"},
		}
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"plan": map[string]any{
				"type":     "array",
				"minItems": 1,
				"maxItems": spanLen,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"round": map[string]any{"type": "integer"},
						"card":  card,
					},
					"required": []string{"round", "card"},
				},
			},
		},
		"required": []string{"plan"},
	}
}

// cardOf reads the card back out of a canonical Goofspiel move ("card:7").
//
// Goes through the canonical string rather than the raw tool arguments on purpose: the
// canonical form is what the platform stores and compares, so reading THAT is what makes the
// lab's expectation identical to the server's.
func cardOf(move string) (int, bool) {
	rest, ok := strings.CutPrefix(move, "card:")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil
}
