package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/movebind"
)

// Completion binding for MAFIA and MONOPOLY.
//
// # Why this exists
//
// -bind was Goofspiel-only: it asks for a `play_card` tool call, which is Goofspiel's move
// shape. So the lab could never produce an LLM-backed Mafia or Monopoly match, and combined
// with the four places the harness silently substituted Goofspiel, it meant NO real agent had
// meaningfully played either game. The lab's own Mafia agents are rule-based policies that say
// "Observing the table." — the mechanics were exercised, the model never was.
//
// The platform side was already ready. movebind knows all three games: ToolFor(game) gives the
// tool name, Canon(game, tc) builds the bound form, and Mafia's args are `kind` + optional
// `target` with an ABSENT target meaning "no target" rather than seat 0. Nothing here invents
// a convention; it sends what movebind already parses.
//
// # What binding proves, and why the schema has to match exactly
//
// The gateway extracts the move from the model's OWN tool call and the match refuses a
// submitted move that disagrees. If this asked for arguments movebind does not read, the call
// would look bound and bind nothing — a verified badge for an unverified decision. So the
// argument names come from movebind.Canon's own reads: `kind`, `target`, `property`, `amount`.

// gameToolSchema is the JSON Schema for one game's move tool.
//
// Mafia deliberately includes `text` — the PUBLIC speech that rides with the action. That is
// the one-call path: one model call yields the decision and what the table hears. Without it a
// bound Mafia agent could act but never talk, which is most of the game.
// viewPrompt is the prompt a BENCHMARKED seat sends: the seat's whole view, verbatim.
//
// The bound prompt used to be a one-line summary — "Goofspiel round 4, legal cards [...], the
// prize is worth 9" — for all three games. That is enough to produce a legal move and useless
// as a measurement: with no history, no scores and no opponent state in the prompt, every model
// is guessing from the same three facts, so the run cannot distinguish a model that reasons
// about the game from one that picks a plausible number. A benchmark built on it would report
// differences that are noise, which is worse than reporting nothing.
//
// It also makes the questions this benchmark exists to answer unanswerable. "Does the model use
// history?" and "does it plan over a long horizon?" are questions about what it does with
// context it was given; a prompt that carries no context cannot ask them.
//
// Same wrapper as internal/labagent.PromptFor, deliberately: the certification harness and the
// match harness must put the same bytes in front of a model or their numbers are not comparable.
func viewPrompt(raw []byte, head string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		// Fall back to the summary rather than failing the turn: an unparseable view is a bug
		// worth surfacing, but forfeiting the round would corrupt the match on top of it.
		return head
	}
	return head + "\n\nHere is your full view of the current turn:\n" + buf.String() +
		"\n\nDecide your move and report it by calling the provided tool. Do not answer in prose."
}

func gameToolSchema(game string) map[string]any {
	if game == movebind.GameMafia {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"description": "One action kind from the legal list for this phase.",
				},
				"target": map[string]any{
					"type": "integer",
					// Stated in the schema because the default is a trap: seat 0 is a real
					// player, so a forgotten target must be OMITTED, never sent as 0.
					"description": "Seat to act on. OMIT entirely for no target — do not send 0, which is a real player.",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "What you say to the table, in character. Public.",
				},
			},
			"required": []string{"kind"},
		}
	}
	return moveToolSchema(1) // goofspiel
}

// boundAction is what the model chose, plus the canonical form the platform will compare.
type boundAction struct {
	Kind     string
	Target   int
	Text     string
	Property int
	Amount   int
	// Canon is the bound form as movebind.Canon would build it. Carried so the caller can log
	// exactly what the platform is expected to have extracted.
	Canon string
}

// decideGameThroughGateway asks the model for one action through the LLM Gateway, so the turn
// is completion-bound.
//
// Returns an error rather than falling back to a policy: a run that quietly played a
// rule-based move while reporting a bound one would measure nothing and claim success. The
// caller decides whether to fall back, and says so in the log when it does.
func (a *labAgent) decideGameThroughGateway(game, matchID string, turn, seat int, proof string, legal []string, prompt string) (boundAction, error) {
	if BindGatewayBase == "" {
		return boundAction{}, fmt.Errorf("gateway base URL not set")
	}
	if proof == "" {
		// Same refusal as the Goofspiel path, for the same reason: without a turn proof the
		// decision CANNOT be bound, and the usual cause is TURN_PROOF_SECRET unset on the
		// server — exactly the misconfiguration a "successful" run must not hide.
		return boundAction{}, fmt.Errorf(
			"the turn view carried no turn_proof, so this decision CANNOT be bound " +
				"(is TURN_PROOF_SECRET set on the server?)")
	}

	tool := movebind.ToolFor(game)
	schema := gameToolSchema(game)
	instruction := fmt.Sprintf("%s Legal actions: %s. Report your choice with the %s tool.",
		prompt, strings.Join(legal, ", "), tool)

	reqBody := map[string]any{
		"model":      "claude-opus-4",
		"max_tokens": bindMaxTokens,
		"stream":     BindStream,
		"tools": []map[string]any{{
			"name": tool, "description": "Take one action for this turn.", "input_schema": schema,
		}},
		"tool_choice": map[string]any{"type": "tool", "name": tool},
		"messages":    []map[string]any{{"role": "user", "content": instruction}},
	}
	// OPENAI WIRE for anything that is not Anthropic — the tool nests under `function` and the
	// forcing field is named differently. Sending the Anthropic shape is a 400, not a silent
	// mis-parse, but a 400 still ends the run so it is worth getting right.
	if BindProvider != "" && BindProvider != "anthropic" {
		reqBody = map[string]any{
			"model":      a.modelFor(),
			"max_tokens": bindMaxTokens,
			"stream":     BindStream,
			"tools": []map[string]any{{
				"type": "function",
				"function": map[string]any{
					"name": tool, "description": "Take one action for this turn.", "parameters": schema,
				},
			}},
			"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": tool}},
			"messages":    []map[string]any{{"role": "user", "content": instruction}},
		}
	}

	body, _ := json.Marshal(reqBody)
	// The gateway routes /v1/gw/{provider}/*, so the path after the provider is the provider's
	// OWN path. Same construction as the Goofspiel path rather than a second guess at it.
	route := "/v1/gw/anthropic/v1/messages"
	if BindProvider != "" && BindProvider != "anthropic" {
		route = "/v1/gw/" + BindProvider + "/v1/chat/completions"
	}
	req, err := http.NewRequest(http.MethodPost, BindGatewayBase+route, bytes.NewReader(body))
	if err != nil {
		return boundAction{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if BindKey != "" {
		req.Header.Set("Authorization", "Bearer "+BindKey)
	} else {
		req.Header.Set("x-api-key", "lab-provider-key")
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	// Pyyol identity and the per-turn proof — the EXACT header names the Goofspiel bind path
	// uses. Inventing plausible ones here would have produced calls the gateway could not
	// attribute, which look like a working run and bind nothing.
	req.Header.Set("X-Pyyol-Key", a.AgentKey)
	req.Header.Set("X-Pyyol-Match", matchID)
	req.Header.Set("X-Pyyol-Turn", fmt.Sprint(turn))
	req.Header.Set("X-Pyyol-Proof", proof)

	resp, err := (&http.Client{Timeout: 180 * time.Second}).Do(req)
	if err != nil {
		return boundAction{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Content []struct {
			Type  string         `json:"type"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return boundAction{}, fmt.Errorf("decode gateway response: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		// Named explicitly: a 429 is now evidence the platform accepts for a deadline
		// extension, so a run should say "throttled" rather than "failed".
		return boundAction{}, fmt.Errorf("provider rate-limited this turn (429)")
	}

	args, ok := toolArgsFrom(out.Content, out.Choices, tool)
	if !ok {
		return boundAction{}, fmt.Errorf(
			"the completion carried no %s tool call, so nothing was bound", tool)
	}

	act := boundAction{Kind: strings.ToLower(strings.TrimSpace(stringOf(args, "kind")))}
	if act.Kind == "" {
		return boundAction{}, fmt.Errorf("the %s tool call named no kind", tool)
	}
	act.Text = stringOf(args, "text")
	if act.Text == "" {
		act.Text = stringOf(args, "rationale")
	}
	// ABSENT target ⇒ NoTarget, never 0. Mirrors movebind.Canon exactly; defaulting to 0 here
	// would bind an action against a real player the model never named.
	if t, has := intOf(args, "target"); has {
		act.Target = t
	} else {
		act.Target = movebind.NoTarget
	}
	act.Property, _ = intOf(args, "property")
	act.Amount, _ = intOf(args, "amount")

	// The canonical form, built the same way the platform will build it, so a mismatch shows up
	// here as a log line rather than later as an unexplained rejected move.
	// Mafia is the only game with a canonical form to build; Goofspiel binds on the card
	// itself. A switch here would be a switch of one, so this is an if until a second
	// structured game arrives.
	if game == movebind.GameMafia {
		act.Canon = movebind.CanonMafia(act.Kind, act.Target)
	}
	return act, nil
}

// toolArgsFrom pulls the tool arguments out of either wire shape.
func toolArgsFrom(
	anthropic []struct {
		Type  string         `json:"type"`
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	},
	openai []struct {
		Message struct {
			ToolCalls []struct {
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	},
	tool string,
) (map[string]any, bool) {
	for _, c := range anthropic {
		if c.Type == "tool_use" && c.Name == tool && c.Input != nil {
			return c.Input, true
		}
	}
	for _, ch := range openai {
		for _, tc := range ch.Message.ToolCalls {
			if tc.Function.Name != tool {
				continue
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err == nil {
				return args, true
			}
		}
	}
	return nil, false
}

func stringOf(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// intOf reports whether the key was PRESENT, because absent and zero mean different things
// for a Mafia target.
func intOf(m map[string]any, k string) (int, bool) {
	switch v := m[k].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}
