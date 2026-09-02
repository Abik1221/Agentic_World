// Package movebind extracts the MOVE a model actually produced out of a provider's
// completion, and reduces it to a canonical string the match engine can compare against
// what the agent later submitted.
//
// # The gap this closes
//
// A turn proof (internal/turnproof) is an HMAC over (agent, match, round). It proves a real
// model call was made FOR this decision — the agent could not have minted the token for a
// turn it was not handed. It does NOT prove the model's answer is the move that got played.
// An agent could call the model, discard the response, and submit a scripted move: every
// call bound, every proof valid, the leaderboard measuring a script.
//
// Closing that needs the platform to know what the model SAID, independently of what the
// agent later claims. The gateway already sees the completion — it is the only party that
// sees both the model's output and the submitted move — so the extraction belongs there,
// and the comparison belongs where the move is applied.
//
// # Why a tool call and not prose
//
// Parsing a move out of free text is a losing game: "I'll play the 7" and "seven, I think"
// and "7." are all the same move to a human and three different strings to a parser, so
// enforcement would fail honest agents constantly. Every current provider supports
// structured tool calls, and a tool call has exactly one machine-readable shape. So the
// contract is: the move arrives as a tool call or the decision is simply not bound.
//
// # The honest residual
//
// This proves the model emitted this move. It does not prove the PROMPT was a fair
// description of the game — an agent can engineer a prompt toward an answer it already
// wanted. That is prompt engineering, which is strategy on this platform, not fraud, and
// deliberately not something this package tries to detect.
//
// # Why the canonical form omits server-known state
//
// Mafia's move signature covers "night|kill|3" — phase included — but the phase is the
// SERVER's state, not the model's choice. Binding it here would mean an SDK that labelled
// the phase differently produced a mismatch and an honest turn was rejected. So a bound
// move carries only what the model actually chose (the verb and its target); the service
// compares that against the same reduction of the submitted action.
//
// Stdlib only, and no dependency on the database or the gateway — the one seam that reaches
// storage is an interface declared here (Reader). The gateway, the three game services and
// the replay verifier all reduce moves with this identical code, so none of them can drift
// into rejecting a move another one accepts.
package movebind

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
)

// Tool names the SDKs must use. One per game, named here so the two SDKs, the gateway and
// the game services cannot drift — a renamed tool would silently stop binding every move
// while every test that mocks its own name kept passing.
const (
	ToolGoofspiel = "play_card"
	ToolMafia     = "mafia_action"
)

// Game slugs, matching matches.game.
const (
	GameGoofspiel = "goofspiel"
	GameMafia     = "mafia"
)

// ToolFor returns the tool name that carries a move for this game, or "" if the game has no
// bound-move contract yet. A game absent here binds nothing rather than binding wrongly.
func ToolFor(game string) string {
	switch game {
	case GameGoofspiel:
		return ToolGoofspiel
	case GameMafia:
		return ToolMafia
	}
	return ""
}

// ToolCall is one structured call the model emitted.
type ToolCall struct {
	Name string
	// Args is the decoded argument object. Providers disagree about whether arguments
	// arrive as a JSON string (OpenAI) or an object (Anthropic, Google); both land here
	// decoded, so callers never have to know which provider served the call.
	Args map[string]any
}

// CompletionHash is the digest of the completion bytes the gateway observed.
//
// Over the RAW body, not the extracted move: the hash's job is to pin the exact response the
// platform saw, so a dispute can be settled by re-deriving the move from it. Hashing only
// the move would make the digest a restatement of a value we already store.
func CompletionHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Canon reduces a move tool call to the canonical string a bound decision stores.
//
// Returns ok=false when the call is not this game's move tool, or its arguments do not
// describe a move. A caller must treat that as "nothing was bound" and never as "the move
// was wrong" — an unparseable call is our failure to understand, not evidence of cheating.
func Canon(game string, tc ToolCall) (string, bool) {
	if tc.Name != ToolFor(game) || tc.Name == "" {
		return "", false
	}
	switch game {
	case GameGoofspiel:
		card, ok := intArg(tc.Args, "card")
		if !ok {
			return "", false
		}
		return CanonGoofspiel(card), true
	case GameMafia:
		kind := strings.ToLower(strings.TrimSpace(stringArg(tc.Args, "kind")))
		if kind == "" {
			return "", false
		}
		// An ABSENT target means "no target", not seat 0. Seat 0 is a real player, so
		// defaulting a missing argument to 0 would bind an action against a specific
		// player the model never named — and then reject the honest untargeted move that
		// followed. The SDK's own dataclass uses -1 for the same reason.
		target, has := intArg(tc.Args, "target")
		if !has {
			target = NoTarget
		}
		return CanonMafia(kind, target), true
	}
	return "", false
}

// RoundMove is one round's canonical move within the span a single completion covers.
type RoundMove struct {
	Round int
	Move  string
}

// MaxSpanRounds caps how many rounds one completion may claim to have decided.
//
// A bound rather than an unbounded list because the plan is attacker-supplied: without a cap
// a single call could assert a hundred thousand rounds and turn one request into that many
// database writes. Comfortably above any real game — Goofspiel is 13 rounds and Monopoly's
// turn cap is well inside this — so a legitimate agent never meets it.
const MaxSpanRounds = 64

// planKey is the argument that carries a multi-round decision. Named once: the SDKs, the
// conformance fixtures and this extractor all have to agree, and a mismatch would silently
// fall back to single-round binding rather than fail loudly.
const planKey = "plan"

// CanonPlan reduces a move tool call to every round it decided.
//
// # Why a completion may cover more than one round
//
// Coverage used to count CALLS: one completion bound exactly one round. That made the honest
// floor for an agent that batches — one call planning three rounds — about 33%, measured on
// real staked tables. Phase 4 exists to REWARD batching as cost optimisation, so the metric
// scored the cheapest honest agent as the least verified one, and no threshold reconciles
// that. The fix is to make coverage mean "decisions a model made" rather than "calls made":
// if one completion legitimately decided rounds 4, 5 and 6, those three rounds ARE
// model-backed and each should count.
//
// # Why claiming a span is safe to allow
//
// A span is a COMMITMENT, not a free coverage win. Each round in it is bound to a specific
// move, and match-time enforcement is unchanged — submitting anything else for round 5 is
// rejected exactly as a substitution is. So an agent that over-claims has only tied its own
// hands, and one that plays what it claimed genuinely played what its model chose. There is
// no version of this an agent profits from without actually letting the model decide.
//
// # The one thing a span must never do: reach backwards
//
// Rounds before provenRound are DROPPED. The turn proof attests the call belongs to
// provenRound; earlier rounds have already been played, so writing a binding over them would
// let an agent retroactively claim coverage for turns it played unbound — inflating the very
// number the ranked gate reads. Forward claims are self-limiting because they are enforced;
// backward claims are not enforced at all, because those moves are already sealed.
//
// Returns ok=false for "nothing to bind", which every caller must treat as absence rather
// than as a mismatch. A plan naming the same round twice is rejected WHOLE: two different
// moves for one slot has no honest reading, and picking either one would be the platform
// guessing on the agent's behalf.
func CanonPlan(game string, tc ToolCall, provenRound int) ([]RoundMove, bool) {
	if tc.Name != ToolFor(game) || tc.Name == "" {
		return nil, false
	}
	entries, ok := planEntries(tc.Args)
	if !ok {
		// No plan: the ordinary single-round call, which stays exactly as it was.
		move, ok := Canon(game, tc)
		if !ok {
			return nil, false
		}
		return []RoundMove{{Round: provenRound, Move: move}}, true
	}
	if len(entries) > MaxSpanRounds {
		return nil, false
	}
	seen := make(map[int]bool, len(entries))
	out := make([]RoundMove, 0, len(entries))
	for _, args := range entries {
		round, has := intArg(args, "round")
		if !has || round < provenRound {
			// Backward or unlabelled: skipped, never an error. A model that emitted one
			// malformed entry has still honestly decided the others.
			continue
		}
		if seen[round] {
			return nil, false
		}
		move, ok := Canon(game, ToolCall{Name: tc.Name, Args: args})
		if !ok {
			continue
		}
		seen[round] = true
		out = append(out, RoundMove{Round: round, Move: move})
	}
	if len(out) == 0 {
		return nil, false
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Round < out[j].Round })
	return out, true
}

// planEntries reads the per-round argument objects out of a plan array.
//
// ok=false means this call carries no plan at all, which is the common case and must fall
// back to single-round binding rather than bind nothing.
func planEntries(args map[string]any) ([]map[string]any, bool) {
	raw, present := args[planKey]
	if !present {
		return nil, false
	}
	list, isList := raw.([]any)
	if !isList || len(list) == 0 {
		return nil, false
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		obj, isObj := item.(map[string]any)
		if !isObj {
			continue
		}
		out = append(out, obj)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// CanonGoofspiel is the bound form of a Goofspiel move: the card, and nothing else.
func CanonGoofspiel(card int) string { return "card:" + strconv.Itoa(card) }

// NoTarget is the wire convention for "this action names no seat".
//
// NOT zero, and this is the one number in this package worth reading twice: seat 0 is a real
// player. The SDK's MafiaMove dataclass defaults to -1 for exactly this reason — a forgotten
// target used to act silently on seat 0 — and the canonical form has to agree with it or
// every untargeted action would bind as an action against that player.
const NoTarget = -1

// CanonMafia is the bound form of a Mafia action: the verb and its target seat.
//
// The PHASE is deliberately excluded — see the package comment.
//
// Negative targets collapse to one token; ZERO DOES NOT. That asymmetry follows the engine
// rather than a convenience: Alive is a map[int]bool, so seat 0 is an ordinary player and a
// negative target simply fails the liveness lookup and is refused as illegal. Abstaining is
// its own action KIND, not a sentinel target. So "no target" is only ever an absent or
// negative field, and seat 0 must stay distinguishable from it — collapsing 0 too would let a
// move against that one player be substituted for doing nothing.
func CanonMafia(kind string, target int) string {
	t := strconv.Itoa(target)
	if target < 0 {
		t = "none"
	}
	return strings.ToLower(strings.TrimSpace(kind)) + ":" + t
}

// CanonMonopoly is the bound form of a Monopoly action: verb, property, amount.
//
// All three are always rendered, including zeros. Omitting an absent field would let
// "mortgage property 0 for 50" and "mortgage property 50 for 0" reduce to the same string,
// and two different decisions sharing one canonical form is the one failure this whole
// mechanism cannot tolerate.
//
// The TRADE payload is deliberately excluded. A trade is a nested structure rather than a
// scalar choice, so a tool schema that expressed it faithfully would be large and easy for a
// model to render in a form that differs cosmetically from the submitted one — and every
// such difference would REJECT an honest turn. Trades therefore bind on their verb only,
// which is weaker than the rest of the game and is the correct trade-off while the rule is
// "never wrongly reject".
func CanonMonopoly(kind string, property, amount int) string {
	return strings.ToLower(strings.TrimSpace(kind)) + ":" +
		strconv.Itoa(property) + ":" + strconv.Itoa(amount)
}

// stringArg reads a string argument, tolerating a number the model quoted or left bare.
func stringArg(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

// intArg reads an integer argument.
//
// Tolerant of a model that emitted "7" instead of 7, because that is a formatting habit
// rather than a different decision, and rejecting it would turn a cosmetic difference into
// an unbound turn. NOT tolerant of a fractional value: 7.5 is not a card, and rounding it
// would invent a move the model did not make.
func intArg(args map[string]any, key string) (int, bool) {
	switch v := args[key].(type) {
	case float64:
		if v != float64(int(v)) {
			return 0, false
		}
		return int(v), true
	case int:
		return v, true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// Extract pulls the LAST move tool call out of a completion body.
//
// # Structural, not per-provider
//
// This used to decode four named shapes: OpenAI chat, OpenAI Responses, Anthropic, Google. That
// approach loses by construction. Providers appear faster than a table can be maintained, every
// self-hosted server (vLLM, Ollama, llama.cpp, LM Studio, SGLang, TGI) ships its own dialect,
// and an unlisted provider fails SILENTLY — the turn is simply never bound, so the agent looks
// unverifiable and nobody learns why. Adding the fifth, sixth and seventh shape is the wrong
// shape of fix.
//
// What is actually invariant is the STRUCTURE. Every provider that has ever expressed a tool
// call has expressed it as a name beside an arguments blob, as SIBLINGS in one object:
//
//	OpenAI      {"function": {"name": "play_card", "arguments": "{\"card\":7}"}}
//	Responses   {"type": "function_call", "name": "play_card", "arguments": "{...}"}
//	Anthropic   {"type": "tool_use", "name": "play_card", "input": {"card": 7}}
//	Google      {"functionCall": {"name": "play_card", "args": {"card": 7}}}
//	Bedrock     {"toolUse": {"name": "play_card", "input": {"card": 7}}}
//	Cohere      {"function": {"name": "play_card", "arguments": "{...}"}}
//
// So the walk looks for that structure anywhere in the document, and a provider nobody has
// heard of works on the day it ships.
//
// # Why a general walk is SAFE here
//
// A false positive would be far worse than a miss: a wrong extracted move REJECTS an honest
// turn, while a miss merely leaves it unverified. Three things bound the risk.
//
// The tool NAME is the discriminator, and it is ours — an object called "play_card" carrying
// arguments is the model's move call and essentially nothing else. A schema echoed back in the
// response is the one near-miss, and "parameters" is deliberately NOT accepted as an arguments
// key for exactly that reason: a JSON Schema decodes to properties/type, yields no card, and
// falls through to unbound rather than to a wrong move.
//
// Every failure path returns not-found. Unparseable body, unknown structure, arguments that do
// not decode: all of them mean "the platform has nothing to say about this move".
func Extract(body []byte, toolName string) (ToolCall, bool) {
	if toolName == "" || len(body) == 0 {
		return ToolCall{}, false
	}
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		return ToolCall{}, false
	}
	found := findToolCalls(doc, toolName)
	if len(found) == 0 {
		return ToolCall{}, false
	}
	// LAST, not first: a model may emit a scratchpad call, correct itself, and call again, and
	// the decision it stands behind is the one it finished with. Taking the first would bind a
	// move the model itself abandoned.
	return found[len(found)-1], true
}

// argsKeys are the sibling fields that carry a tool call's arguments, across every provider
// shape seen so far.
//
// "parameters" is EXCLUDED on purpose — it is the JSON Schema keyword, so accepting it would
// let a tool DEFINITION echoed back in a response be read as a tool CALL.
var argsKeys = []string{"arguments", "input", "args"}

// findToolCalls collects every (name == toolName, arguments) pair in the document, in document
// order.
//
// Order matters and is why arrays are walked in sequence and object keys in sorted order: the
// caller takes the LAST call, so a nondeterministic walk would make which move gets bound
// depend on Go's map iteration seed. That would be a coin flip deciding whether an honest turn
// is accepted.
func findToolCalls(node any, toolName string) []ToolCall {
	var out []ToolCall
	switch n := node.(type) {
	case []any:
		for _, item := range n {
			out = append(out, findToolCalls(item, toolName)...)
		}
	case map[string]any:
		// Is THIS object a tool call for our tool?
		if name, ok := n["name"].(string); ok && name == toolName {
			for _, k := range argsKeys {
				raw, present := n[k]
				if !present {
					continue
				}
				if args, ok := decodeArgsValue(raw); ok {
					out = append(out, ToolCall{Name: toolName, Args: args})
					break
				}
			}
		}
		// Recurse in a deterministic order regardless of what matched above, so a nested call
		// later in the document still wins.
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, findToolCalls(n[k], toolName)...)
		}
	}
	return out
}

// decodeArgsValue accepts an already-decoded object, or a JSON string containing one.
//
// OpenAI-family providers send arguments as a STRING; Anthropic, Google and Bedrock send an
// object. Both land here so no caller needs to know which.
func decodeArgsValue(raw any) (map[string]any, bool) {
	switch v := raw.(type) {
	case map[string]any:
		return v, true
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil, false
		}
		var obj map[string]any
		if json.Unmarshal([]byte(s), &obj) == nil {
			return obj, true
		}
	}
	return nil, false
}

// decodeArgs accepts either a JSON object or a JSON string containing one. Retained for the
// streaming path, which assembles raw bytes rather than decoded values.
func decodeArgs(raw []byte) (map[string]any, bool) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil, false
	}
	var asObject map[string]any
	if json.Unmarshal(raw, &asObject) == nil {
		return asObject, true
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		if json.Unmarshal([]byte(asString), &asObject) == nil {
			return asObject, true
		}
	}
	return nil, false
}

// ExtractStream pulls the last move tool call out of a captured SSE stream.
//
// Streaming needs its own path because a tool call does not always arrive whole: some providers
// ship the arguments as a run of JSON fragments that mean nothing individually and only parse
// once concatenated. Without this, every streaming agent would be permanently unbound — and
// streaming is the normal choice for anything latency-sensitive, which on a platform that scores
// latency is most of them.
//
// Two general strategies rather than a per-provider decode, for the same reason Extract is
// structural:
//
//  1. WHOLE-CALL PER FRAME. Some providers (Google, and any server that emits complete deltas)
//     put a finished tool call in a single frame. So every frame is offered to the same
//     structural walk Extract uses; if a frame contains a complete call, it counts.
//  2. FRAGMENT ACCUMULATION. Others split the arguments. Fragments are collected per block
//     index — a name declaration opens a block, and any later string fragment on that index is
//     appended. This covers OpenAI's delta.tool_calls[].function.arguments and Anthropic's
//     content_block_delta.input_json_delta.partial_json without naming either.
//
// A stream that is cut off mid-arguments accumulates something that does not parse, and
// therefore binds nothing — which is the required direction: half of an argument blob must never
// decode to a different move than the model chose.
func ExtractStream(sse []byte, toolName string) (ToolCall, bool) {
	if toolName == "" || len(sse) == 0 {
		return ToolCall{}, false
	}
	type acc struct {
		name string
		buf  strings.Builder
	}
	blocks := map[int]*acc{}
	var order []int
	at := func(i int) *acc {
		a, seen := blocks[i]
		if !seen {
			a = &acc{}
			blocks[i] = a
			order = append(order, i)
		}
		return a
	}

	var whole []ToolCall
	// active is the block index most recently opened by a name declaration, so a provider that
	// omits an index on its argument frames still accumulates onto the right block.
	active := 0

	for _, line := range strings.Split(string(sse), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var frame any
		if json.Unmarshal([]byte(payload), &frame) != nil {
			continue // a frame we cannot read is skipped, never fatal
		}
		// Strategy 1: a complete call in this frame.
		whole = append(whole, findToolCalls(frame, toolName)...)
		// Strategy 2: fragments.
		if idx, named := streamOpensTool(frame, toolName); named {
			active = idx
			at(idx).name = toolName
		}
		for _, frag := range streamArgFragments(frame) {
			if a, open := blocks[active]; open && a.name == toolName {
				a.buf.WriteString(frag)
			}
		}
	}

	// A whole call seen in a frame wins over accumulation: it is unambiguous, whereas a
	// fragment buffer is only as good as our guess about which block it belonged to.
	if len(whole) > 0 {
		return whole[len(whole)-1], true
	}
	var found ToolCall
	var ok bool
	for _, i := range order {
		a := blocks[i]
		if a.name != toolName {
			continue
		}
		if args, aok := decodeArgs([]byte(a.buf.String())); aok {
			found, ok = ToolCall{Name: toolName, Args: args}, true
		}
	}
	return found, ok
}

// streamOpensTool reports whether this frame declares our tool, and on which block index.
//
// Structural: anywhere in the frame, an object whose "name" is our tool. The index is read from
// that object or its nearest enclosing object, defaulting to 0 for providers that do not use one.
func streamOpensTool(node any, toolName string) (int, bool) {
	idx, found := 0, false
	var walk func(n any, inheritedIdx int)
	walk = func(n any, inheritedIdx int) {
		switch v := n.(type) {
		case []any:
			for _, item := range v {
				walk(item, inheritedIdx)
			}
		case map[string]any:
			cur := inheritedIdx
			if f, ok := numeric(v["index"]); ok {
				cur = int(f)
			}
			if name, ok := v["name"].(string); ok && name == toolName {
				idx, found = cur, true
			}
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(v[k], cur)
			}
		}
	}
	walk(node, 0)
	return idx, found
}

// argFragmentKeys are the fields that carry a PARTIAL arguments string in a stream.
//
// Matched by key name rather than by provider: "partial_json" is Anthropic's, "arguments" is
// OpenAI's delta field, and both mean the same thing. A provider inventing a third name is one
// entry here rather than a new decoder.
var argFragmentKeys = map[string]bool{
	"partial_json": true,
	"arguments":    true,
	"args_delta":   true,
	"input_delta":  true,
}

// streamArgFragments collects every partial-arguments string in a frame, in deterministic order.
func streamArgFragments(node any) []string {
	var out []string
	switch v := node.(type) {
	case []any:
		for _, item := range v {
			out = append(out, streamArgFragments(item)...)
		}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if argFragmentKeys[strings.ToLower(k)] {
				if s, ok := v[k].(string); ok && s != "" {
					out = append(out, s)
				}
				continue
			}
			out = append(out, streamArgFragments(v[k])...)
		}
	}
	return out
}

// numeric reads a JSON number regardless of how it decoded.
func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// Describe renders a bound move for a log or an error a developer will read.
func Describe(game, move string) string {
	if move == "" {
		return fmt.Sprintf("%s: (no move bound)", game)
	}
	return fmt.Sprintf("%s move %q", game, move)
}

// ErrMoveNotFromModel is returned when a submitted move contradicts the one the model
// produced for that turn. It is a rejection of the MOVE, not of the agent: the turn does not
// apply, and the agent may submit the move its model actually chose.
var ErrMoveNotFromModel = errors.New("submitted move does not match the model's own output for this turn")

// Check is THE completion-binding rule, written once because three game services apply it
// and three copies of a rule this consequential would drift.
//
// The asymmetry is the entire design:
//
//   - NOT BOUND (bound=false) → allow. The platform observed no move for this turn: the agent
//     does not route through the gateway, its completion carried no move tool call, or the
//     response was too large to capture. None of that is evidence of anything, and rejecting
//     on it would void the play of every honest developer who has not adopted the tool
//     contract — which today is all of them. Coverage is Phase 2's question, not this one's.
//   - BOUND AND EQUAL → allow. The ordinary path.
//   - BOUND AND DIFFERENT → reject. This is the only case that rejects, and it is
//     unambiguous: the platform holds an HMAC-attested record that the model answered X, and
//     the agent submitted Y. There is no honest reading in which the model's answer drove
//     this move.
//
// Stated as one function so the "absence never rejects" half cannot be dropped by a caller
// who only remembered the enforcement half — which is the shape of the mistake that turns an
// anti-cheat control into an outage.
func Check(extracted string, bound bool, submitted string) error {
	if !bound || extracted == "" {
		return nil
	}
	if extracted == submitted {
		return nil
	}
	return fmt.Errorf("%w: model produced %q, agent submitted %q",
		ErrMoveNotFromModel, extracted, submitted)
}

// Reader reports the move a model produced for one decision. Satisfied by
// *store.LLMGatewayRepo.
//
// A narrow port, declared next to the rule that consumes it: the three game services need
// exactly this one fact from the whole verified-inference pipeline, and keeping the
// dependency this small is what stops a gateway problem from being able to break move
// submission.
type Reader interface {
	// ExtractedMove returns the canonical move the model emitted and whether anything is
	// bound. ok=false MUST mean "nothing to compare", never "mismatch".
	ExtractedMove(ctx context.Context, matchID, agentPublicID string, round int) (string, bool, error)
}

// Enforce is the whole match-time control: read what the model produced, compare, reject
// only on disagreement.
//
// Written ONCE and shared by Goofspiel, Mafia and Monopoly. Three copies would each have to
// remember two easily-forgotten halves — that absence never rejects, and that a read error
// fails OPEN — and the copy that forgot either one would be the one that voided honest
// matches. The same reasoning that put the settlement rule in internal/integrity rather than
// in three engines.
//
// FAILS OPEN on a read error, matching integrity.Evaluate: a database hiccup must not become
// a refused move. A cheat that slips through is still recorded and reviewable; a wrongly
// rejected move is a developer losing a round they played honestly.
//
// A nil reader disables enforcement entirely, which is the pre-existing behaviour.
func Enforce(ctx context.Context, r Reader, log *slog.Logger, game, matchID, agentPublicID string, round int, submitted string) error {
	if r == nil {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	extracted, bound, err := r.ExtractedMove(ctx, matchID, agentPublicID, round)
	if err != nil {
		log.Warn(game+": completion binding unreadable; applying the move unchecked",
			"match", matchID, "agent", agentPublicID, "round", round, "error", err)
		return nil
	}
	if cerr := Check(extracted, bound, submitted); cerr != nil {
		log.Warn(game+": REJECTED a move that contradicts the model's own output",
			"match", matchID, "agent", agentPublicID, "round", round,
			"model_produced", extracted, "agent_submitted", submitted)
		return cerr
	}
	return nil
}
