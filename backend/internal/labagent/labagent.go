// Package labagent is the ONE agent the Lab benchmark runs, pointed at many models.
//
// # The experiment
//
// The Lab benchmark is a controlled experiment, not a tournament. A single implementation —
// identical code, identical prompt, identical tool schema, identical parse — is aimed at a
// different provider and model for each run. Because only the model varies, a difference
// between two runs is attributable to the model, and the model-versus-scaffold confound that
// makes every agentic leaderboard uninterpretable is closed by construction rather than by
// statistical adjustment.
//
// That is also why this package is deliberately dull. Every clever thing one could add — a
// better prompt, retries, self-consistency, a scratchpad — is scaffold, and scaffold that
// helps one model more than another silently becomes part of what the benchmark measures. If
// the prompt changes, it changes for every model at once and the spec hash changes with it.
//
// # Prompting is conformance-pinned, not invented here
//
// sdk/conformance/prompt_for.json is read by the Python and JS SDKs and now by this package.
// A divergence would not surface as a bug; it would surface as the Lab measuring a different
// question than the SDK asks, while both looked fine. PromptForConformance drives the same
// fixtures the SDKs do.
//
// # Parsing reuses the gateway's extractor
//
// The move is read with movebind.Extract, the same walker the completion-binding path uses.
// It finds a tool call structurally — name beside arguments as siblings — rather than by
// enumerating providers, so a model served through any OpenAI-compatible, Anthropic, Google,
// Bedrock, Cohere, Mistral, vLLM or Ollama shaped endpoint parses identically. Writing a
// second parser here would create exactly the drift the conformance suite exists to prevent.
//
// # What a failure means
//
// A model that returns prose, an unparseable call, or nothing at all gets the engine's
// deterministic fallback — the lowest legal card — and that is recorded as its play. It is
// not an error and not a retry. An agent that cannot emit a structured move is an agent that
// plays badly, and a benchmark that quietly retried until it got a parseable answer would be
// measuring our patience rather than the model.
package labagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/ladder"
	"github.com/agent-arena/arena/internal/movebind"
	"github.com/agent-arena/arena/internal/remoteplay"
)

// PromptFor builds the turn prompt for a view, from the view's own JSON bytes.
//
// Takes bytes rather than a value because KEY ORDER IS PART OF THE CONTRACT. The conformance
// fixture's expected prompts preserve the view's field order — `{"game":...,"round":...,
// "seat":...,"prize":...}` is not alphabetical — so the SDKs serialise the view as given and
// do not sort. An earlier version of this function canonicalised by sorting keys, which
// produced a different prompt from the SDKs for every non-trivial view while passing its own
// tests.
//
// What the fixture actually pins is SEPARATORS: the file's own note records that Python's
// json.dumps defaults to ", "/": " while JS's JSON.stringify emits neither, so the two
// languages built different prompts from the same view. json.Compact gives the no-space form
// both settled on, without touching order.
func PromptFor(viewJSON []byte) (string, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, viewJSON); err != nil {
		return "", fmt.Errorf("labagent: view is not valid JSON: %w", err)
	}
	return "You are playing a match in the Pyyol arena. Here is your view of the current turn:\n" +
		buf.String() +
		"\n\nDecide your move and report it by calling the provided tool. Do not answer in prose.", nil
}

// PromptForValue marshals a value and builds its prompt.
//
// Struct field order is declaration order in Go, which is what the driver wants: the seat
// view is a struct, so its serialisation is stable and matches what the SDKs send.
func PromptForValue(view any) (string, error) {
	raw, err := json.Marshal(view)
	if err != nil {
		return "", err
	}
	return PromptFor(raw)
}

// goofspielTool is the move tool offered to the model.
//
// Named from movebind.ToolFor so the name the model is asked to call is the same name the
// extractor looks for. Hardcoding it here would be a second source of truth for the one
// string that has to match.
func goofspielTool() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        movebind.ToolFor("goofspiel"),
			"description": "Play one card from your hand for this round's prize.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"card": map[string]any{
						"type":        "integer",
						"description": "The card to play. Must be one of legal_actions.",
					},
				},
				"required": []string{"card"},
			},
		},
	}
}

// Model identifies what a run is measuring.
type Model struct {
	Provider string
	Model    string
	// BaseURL is the OpenAI-compatible chat-completions endpoint. Pointed at the platform's
	// own gateway in production, so the call is proxied, costed and completion-bound like
	// any developer's — a Lab result that bypassed the gateway would be exactly the
	// unverified attribution the model board exists to refuse.
	BaseURL string
	APIKey  string
	// Temperature is part of the experiment and therefore part of the run's identity. Not
	// defaulted silently: two runs at different temperatures did not measure the same thing.
	Temperature float64
}

// LLMSource builds a decider that asks a model for each move.
type LLMSource struct {
	M      Model
	Client *http.Client
}

// NewLLMSource returns a source with a bounded client.
//
// The timeout is generous rather than tight because a reasoning model legitimately takes
// tens of seconds, and the ladder's own chess clock — not the HTTP client — is what bounds
// thinking time. A short client timeout would silently convert slow-but-honest models into
// fallback plays and read as poor strategy.
func NewLLMSource(m Model) *LLMSource {
	return &LLMSource{M: m, Client: &http.Client{Timeout: 5 * time.Minute}}
}

// Decider implements labdriver.AgentSource.
func (s *LLMSource) Decider(_ context.Context, _ string, _ ladder.Spec) (remoteplay.Decider, error) {
	if strings.TrimSpace(s.M.BaseURL) == "" {
		return nil, fmt.Errorf("labagent: no base URL; refusing to run a benchmark against " +
			"an unspecified endpoint")
	}
	if strings.TrimSpace(s.M.Model) == "" {
		return nil, fmt.Errorf("labagent: no model name; a certificate must say what it measured")
	}
	return &decider{src: s}, nil
}

type decider struct{ src *LLMSource }

// Decide asks the model once and reads its tool call.
//
// ONCE. No retry, no repair, no best-of-n. Each of those is scaffold that would help some
// models more than others, and the whole point of this package is that the scaffold is
// constant. A model that fails to answer usefully returns 0, and the caller applies the
// engine's deterministic fallback — which is what would have happened at a real table.
func (d *decider) Decide(ctx context.Context, v remoteplay.GoofspielView) (int, error) {
	prompt, err := PromptForValue(v)
	if err != nil {
		return 0, err
	}
	body := map[string]any{
		"model": d.src.M.Model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"tools":       []any{goofspielTool()},
		"tool_choice": "auto",
		"temperature": d.src.M.Temperature,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	url := strings.TrimRight(d.src.M.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.src.M.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+d.src.M.APIKey)
	}

	resp, err := d.src.Client.Do(req)
	if err != nil {
		// A transport failure is not a strategic choice, but it is also not something to
		// paper over: the caller falls back, and the census records it.
		return 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, fmt.Errorf("labagent: %s returned %d: %s",
			d.src.M.Model, resp.StatusCode, truncate(string(out), 200))
	}

	tc, ok := movebind.Extract(out, movebind.ToolFor("goofspiel"))
	if !ok {
		return 0, fmt.Errorf("labagent: %s emitted no usable move tool call", d.src.M.Model)
	}
	card, ok := cardFrom(tc)
	if !ok {
		return 0, fmt.Errorf("labagent: %s called the tool with no readable card", d.src.M.Model)
	}
	return card, nil
}

// cardFrom reads the card out of a tool call.
//
// Accepts a JSON number or a numeric string, because providers disagree and a quoted 7 is
// the same decision as a bare 7 — the conformance suite pins that with
// `quoted_number_is_the_same_decision`. Rejects a fractional value: 7.5 is not a card, and
// silently truncating it would invent a move the model did not make.
func cardFrom(tc movebind.ToolCall) (int, bool) {
	v, ok := tc.Args["card"]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		if n != float64(int(n)) {
			return 0, false
		}
		return int(n), true
	case string:
		var f float64
		if err := json.Unmarshal([]byte(n), &f); err != nil {
			return 0, false
		}
		if f != float64(int(f)) {
			return 0, false
		}
		return int(f), true
	default:
		return 0, false
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
