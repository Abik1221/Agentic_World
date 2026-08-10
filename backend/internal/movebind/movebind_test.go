package movebind

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The provider shapes, all carrying the same decision: play_card(7).
//
// Written as literal wire payloads rather than built from structs on purpose. The thing under
// test is our ability to read what a provider ACTUALLY sends, and a fixture generated from
// our own understanding of the format would pass even if that understanding were wrong.
const (
	openAIChat = `{"id":"c1","choices":[{"message":{"role":"assistant","tool_calls":[
		{"id":"t1","type":"function","function":{"name":"play_card","arguments":"{\"card\":7}"}}]}}],
		"usage":{"prompt_tokens":10,"completion_tokens":5}}`

	openAIResponses = `{"id":"r1","output":[
		{"type":"function_call","name":"play_card","arguments":"{\"card\":7}"}]}`

	anthropicMsg = `{"id":"m1","type":"message","role":"assistant","content":[
		{"type":"text","text":"Thinking about it."},
		{"type":"tool_use","id":"tu1","name":"play_card","input":{"card":7}}],
		"usage":{"input_tokens":10,"output_tokens":5}}`

	googleGen = `{"candidates":[{"content":{"parts":[
		{"functionCall":{"name":"play_card","args":{"card":7}}}]}}]}`
)

func TestExtractReadsEveryProviderShape(t *testing.T) {
	for name, body := range map[string]string{
		"openai_chat":      openAIChat,
		"openai_responses": openAIResponses,
		"anthropic":        anthropicMsg,
		"google":           googleGen,
	} {
		t.Run(name, func(t *testing.T) {
			tc, ok := Extract([]byte(body), ToolGoofspiel)
			if !ok {
				t.Fatalf("no tool call extracted from a %s completion that contains one", name)
			}
			move, ok := Canon(GameGoofspiel, tc)
			if !ok {
				t.Fatalf("tool call did not reduce to a move: %+v", tc)
			}
			if move != "card:7" {
				t.Fatalf("extracted move = %q, want %q", move, "card:7")
			}
		})
	}
}

// A model that revised its answer must bind the REVISION. Binding the first call would let an
// agent pin a move by making a throwaway call, which inverts the control.
func TestExtractTakesTheLastToolCall(t *testing.T) {
	body := `{"choices":[{"message":{"tool_calls":[
		{"function":{"name":"play_card","arguments":"{\"card\":2}"}},
		{"function":{"name":"play_card","arguments":"{\"card\":9}"}}]}}]}`
	tc, ok := Extract([]byte(body), ToolGoofspiel)
	if !ok {
		t.Fatal("expected a tool call")
	}
	if move, _ := Canon(GameGoofspiel, tc); move != "card:9" {
		t.Fatalf("bound %q, want the model's final answer card:9", move)
	}
}

// Everything unparseable must yield "nothing bound" rather than a wrong move. A wrong
// extracted move does not fail safe — it REJECTS an honest turn.
func TestExtractYieldsNothingRatherThanAWrongMove(t *testing.T) {
	cases := map[string]string{
		"empty":                ``,
		"not json":             `<html>502 Bad Gateway</html>`,
		"no tool call at all":  `{"choices":[{"message":{"content":"I play the 7"}}]}`,
		"a different tool":     `{"choices":[{"message":{"tool_calls":[{"function":{"name":"lookup_rules","arguments":"{}"}}]}}]}`,
		"arguments not an obj": `{"choices":[{"message":{"tool_calls":[{"function":{"name":"play_card","arguments":"7"}}]}}]}`,
		"truncated json":       `{"choices":[{"message":{"tool_calls":[{"function":{"name":"play_card","argum`,
		"fractional card":      `{"content":[{"type":"tool_use","name":"play_card","input":{"card":7.5}}]}`,
		"card key absent":      `{"content":[{"type":"tool_use","name":"play_card","input":{"value":7}}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			tc, ok := Extract([]byte(body), ToolGoofspiel)
			if ok {
				if move, mok := Canon(GameGoofspiel, tc); mok {
					t.Fatalf("bound %q from a completion that does not contain a usable move", move)
				}
			}
		})
	}
}

// A model that wrote "7" instead of 7 made the same decision. Rejecting that formatting habit
// would leave honest agents permanently unbound.
func TestExtractToleratesAQuotedNumber(t *testing.T) {
	body := `{"content":[{"type":"tool_use","name":"play_card","input":{"card":"7"}}]}`
	tc, ok := Extract([]byte(body), ToolGoofspiel)
	if !ok {
		t.Fatal("expected a tool call")
	}
	if move, _ := Canon(GameGoofspiel, tc); move != "card:7" {
		t.Fatalf("bound %q, want card:7", move)
	}
}

// --- Streaming ---------------------------------------------------------------------------
//
// Streaming is where the previous gateway captured nothing at all, so these are the cases
// that decide whether a latency-sensitive agent can be bound.

func TestExtractStreamAssemblesOpenAIFragments(t *testing.T) {
	// Arguments split mid-token across frames, which is what actually arrives on the wire.
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"play_card","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"ca"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"rd\": 1"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"3}"}}]}}]}`,
		`data: [DONE]`,
	}, "\n")
	tc, ok := ExtractStream([]byte(sse), ToolGoofspiel)
	if !ok {
		t.Fatal("no tool call assembled from a stream that carries one")
	}
	if move, _ := Canon(GameGoofspiel, tc); move != "card:13" {
		t.Fatalf("assembled %q, want card:13", move)
	}
}

func TestExtractStreamAssemblesAnthropicFragments(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"message_start","message":{"model":"claude-opus-4","usage":{"input_tokens":10}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"play_card"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"card\""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":": 4}"}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":7}}`,
	}, "\n")
	tc, ok := ExtractStream([]byte(sse), ToolGoofspiel)
	if !ok {
		t.Fatal("no tool call assembled from an Anthropic stream that carries one")
	}
	if move, _ := Canon(GameGoofspiel, tc); move != "card:4" {
		t.Fatalf("assembled %q, want card:4", move)
	}
}

// A stream cut off mid-arguments must bind NOTHING. This is the failure mode that would
// otherwise reject honest turns: half of `{"card": 13}` parses to nothing, but half of
// `{"card": 13, ...}` could parse to a different card than the model chose.
func TestExtractStreamBindsNothingOnATruncatedStream(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"play_card","arguments":"{\"ca"}}]}}]}`,
	}, "\n")
	if tc, ok := ExtractStream([]byte(sse), ToolGoofspiel); ok {
		if move, mok := Canon(GameGoofspiel, tc); mok {
			t.Fatalf("bound %q from a truncated stream", move)
		}
	}
}

// --- Canonical forms ---------------------------------------------------------------------

// Two different decisions must never reduce to one string. If they can, the whole mechanism
// is unsound: a substituted move would compare equal to the model's.
func TestCanonicalFormsAreInjective(t *testing.T) {
	seen := map[string]string{}
	add := func(what, canon string) {
		t.Helper()
		if prev, dup := seen[canon]; dup {
			t.Fatalf("%s and %s both reduce to %q — two decisions sharing one canonical form",
				prev, what, canon)
		}
		seen[canon] = what
	}
	for c := 0; c <= 13; c++ {
		add(fmt.Sprintf("goofspiel card %d", c), "gs/"+CanonGoofspiel(c))
	}
	for _, kind := range []string{"kill", "vote", "protect", "investigate", "pass"} {
		// From NoTarget upward, so the collision that matters is covered: "no target" must
		// not reduce to the same string as an action against seat 0.
		for target := NoTarget; target <= 11; target++ {
			add(fmt.Sprintf("mafia %s %d", kind, target), "mf/"+CanonMafia(kind, target))
		}
	}
	// The collision this pins: property and amount must not be able to trade places.
	for _, kind := range []string{"buy", "pass", "mortgage", "bid", "build"} {
		for _, prop := range []int{0, 5, 50} {
			for _, amt := range []int{0, 5, 50} {
				add(fmt.Sprintf("monopoly %s p%d a%d", kind, prop, amt),
					"mp/"+CanonMonopoly(kind, prop, amt))
			}
		}
	}
}

// Absence and -1 mean the same thing and must bind identically; seat 0 must not join them.
func TestCanonMafiaSeparatesNoTargetFromSeatZero(t *testing.T) {
	absent := ToolCall{Name: ToolMafia, Args: map[string]any{"kind": "vote"}}
	explicitNone := ToolCall{Name: ToolMafia, Args: map[string]any{"kind": "vote", "target": float64(NoTarget)}}
	seatZero := ToolCall{Name: ToolMafia, Args: map[string]any{"kind": "vote", "target": float64(0)}}

	a, aok := Canon(GameMafia, absent)
	n, nok := Canon(GameMafia, explicitNone)
	z, zok := Canon(GameMafia, seatZero)
	if !aok || !nok || !zok {
		t.Fatal("a well-formed mafia tool call failed to reduce")
	}
	if a != n {
		t.Fatalf("an omitted target (%q) and an explicit -1 (%q) bind differently, so one "+
			"encoding of the same abstain would reject the other", a, n)
	}
	if z == a {
		t.Fatalf("no target and seat 0 both bind to %q — a move against seat 0 could be "+
			"substituted for doing nothing", z)
	}
	if z != "vote:0" {
		t.Fatalf("seat 0 bound as %q, want vote:0", z)
	}
}

func TestCanonIsCaseAndSpaceInsensitiveOnTheVerb(t *testing.T) {
	// A model that emitted "Kill" or " kill " chose the same action. Case-folding here is
	// what stops a cosmetic difference from rejecting an honest turn.
	if CanonMafia("Kill", 3) != CanonMafia("kill", 3) {
		t.Fatal("verb case changes the canonical form")
	}
	if CanonMafia(" kill ", 3) != CanonMafia("kill", 3) {
		t.Fatal("surrounding whitespace changes the canonical form")
	}
}

func TestCanonRejectsAnotherGamesTool(t *testing.T) {
	// A Mafia tool call must not be readable as a Goofspiel move. Otherwise an agent in one
	// game could bind a move in the shape of another.
	tc := ToolCall{Name: ToolMafia, Args: map[string]any{"kind": "kill", "target": float64(3)}}
	if move, ok := Canon(GameGoofspiel, tc); ok {
		t.Fatalf("a mafia tool call reduced to a goofspiel move %q", move)
	}
}

// --- The rule ----------------------------------------------------------------------------

// Check's asymmetry IS the design. These four cases are the whole contract, and the two
// "allow" cases matter more than the reject: getting them wrong voids honest play in bulk.
func TestCheckAllowsAbsenceAndRejectsOnlyDisagreement(t *testing.T) {
	cases := []struct {
		name      string
		extracted string
		bound     bool
		submitted string
		wantErr   bool
	}{
		{"nothing bound — agent does not route", "", false, "card:7", false},
		{"bound but no move in the completion", "", true, "card:7", false},
		{"bound and equal", "card:7", true, "card:7", false},
		{"bound and DIFFERENT — the substitution", "card:7", true, "card:3", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Check(c.extracted, c.bound, c.submitted)
			if c.wantErr {
				if err == nil {
					t.Fatal("a move contradicting the model's output was ALLOWED")
				}
				if !errors.Is(err, ErrMoveNotFromModel) {
					t.Fatalf("error does not wrap ErrMoveNotFromModel: %v", err)
				}
				// The developer has to be able to see WHAT disagreed, or the rejection is
				// unactionable and reads as a platform bug.
				if !strings.Contains(err.Error(), c.extracted) || !strings.Contains(err.Error(), c.submitted) {
					t.Fatalf("error names neither side of the disagreement: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("honest turn rejected: %v", err)
			}
		})
	}
}

// --- Enforce -----------------------------------------------------------------------------

type stubReader struct {
	move  string
	bound bool
	err   error
	calls int
}

func (s *stubReader) ExtractedMove(context.Context, string, string, int) (string, bool, error) {
	s.calls++
	return s.move, s.bound, s.err
}

func TestEnforceFailsOpenOnAReadError(t *testing.T) {
	// A database hiccup must not refuse a move. The opposite choice turns one bad minute in
	// Postgres into every agent on the platform losing every round.
	r := &stubReader{err: errors.New("connection reset")}
	if err := Enforce(context.Background(), r, nil, "match", "m_1", "ag_1", 3, "card:7"); err != nil {
		t.Fatalf("a read error rejected the move: %v", err)
	}
}

func TestEnforceWithNoReaderIsInert(t *testing.T) {
	// The pre-existing behaviour, which is what a deployment without the gateway gets.
	if err := Enforce(context.Background(), nil, nil, "match", "m_1", "ag_1", 3, "card:7"); err != nil {
		t.Fatalf("enforcement without a reader rejected a move: %v", err)
	}
}

// The regression this guards: enforcement that reads the binding and then forgets to compare.
// Without the comparison in Enforce this test fails, which is the point — a guard that cannot
// fail is not a guard.
func TestEnforceRejectsASubstitutedMove(t *testing.T) {
	r := &stubReader{move: "card:7", bound: true}
	err := Enforce(context.Background(), r, nil, "match", "m_1", "ag_1", 3, "card:3")
	if err == nil {
		t.Fatal("Enforce allowed a move the model did not produce")
	}
	if !errors.Is(err, ErrMoveNotFromModel) {
		t.Fatalf("wrong error: %v", err)
	}
	if r.calls != 1 {
		t.Fatalf("read the binding %d times, want exactly 1 (it is on the move path)", r.calls)
	}
}

func TestEnforceAllowsTheModelsOwnMove(t *testing.T) {
	r := &stubReader{move: "card:7", bound: true}
	if err := Enforce(context.Background(), r, nil, "match", "m_1", "ag_1", 3, "card:7"); err != nil {
		t.Fatalf("the model's own move was rejected: %v", err)
	}
}

func TestCompletionHashIsStableAndDistinguishing(t *testing.T) {
	a := CompletionHash([]byte(anthropicMsg))
	if a != CompletionHash([]byte(anthropicMsg)) {
		t.Fatal("hash is not stable over the same bytes")
	}
	if a == CompletionHash([]byte(anthropicMsg+" ")) {
		t.Fatal("hash does not distinguish different bytes")
	}
	if len(a) != 64 {
		t.Fatalf("hash length %d, want 64 hex chars of SHA-256", len(a))
	}
}
