package labagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/agent-arena/arena/internal/ladder"
	"github.com/agent-arena/arena/internal/remoteplay"
)

// TestPromptMatchesTheSDKConformanceFixture is the drift guard.
//
// sdk/conformance/prompt_for.json is read by the Python and JS SDKs. If Go builds a different
// prompt, the Lab measures a different question than the SDK asks and BOTH look fine — the
// exact class of divergence the conformance suite exists to catch, and the reason the repo
// keeps one fixture read by three languages rather than three implementations.
func TestPromptMatchesTheSDKConformanceFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../sdk/conformance/prompt_for.json")
	if err != nil {
		t.Fatalf("read conformance fixture: %v", err)
	}
	var doc struct {
		Cases []struct {
			Name   string          `json:"name"`
			View   json.RawMessage `json:"view"`
			Prompt string          `json:"prompt"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := PromptFor(c.View)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.Prompt {
				t.Errorf("prompt diverges from the SDK fixture\n got: %q\nwant: %q", got, c.Prompt)
			}
		})
	}
}

// TestPromptIsDeterministic. A prompt that varied with map iteration order would make a run
// unreproducible and quietly change what each model was asked.
func TestPromptIsDeterministic(t *testing.T) {
	// A STRUCT, not a map: Go marshals struct fields in declaration order but sorts map
	// keys, and the contract is order-preserving. The driver always passes the seat view
	// struct, so this is the shape that matters.
	view := remoteplay.GoofspielView{Game: "goofspiel", Round: 2, CurrentPrize: 5,
		YourHand: []int{3, 1, 2}, LegalActions: []int{1, 2, 3}}
	first, err := PromptForValue(view)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		got, err := PromptForValue(view)
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Fatalf("prompt is not stable:\n%q\nvs\n%q", got, first)
		}
	}
}

// fakeModel serves one canned chat-completions response.
func fakeModel(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("called %s, want /chat/completions", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("missing or wrong auth header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
}

func decide(t *testing.T, srv *httptest.Server) (int, error) {
	t.Helper()
	src := NewLLMSource(Model{Provider: "test", Model: "m1", BaseURL: srv.URL, APIKey: "k"})
	d, err := src.Decider(context.Background(), "ag", ladder.DefaultSpec())
	if err != nil {
		t.Fatal(err)
	}
	return d.Decide(context.Background(), remoteplay.GoofspielView{
		Game: "goofspiel", Round: 1, CurrentPrize: 3,
		YourHand: []int{1, 2, 3}, LegalActions: []int{1, 2, 3},
	})
}

// TestReadsAToolCallFromEveryProviderShape. The extractor is shared with the gateway, so this
// checks the wiring rather than re-testing movebind — but a Lab agent that could only read
// OpenAI would silently score every other provider as a fallback play.
func TestReadsAToolCallFromEveryProviderShape(t *testing.T) {
	shapes := map[string]string{
		"openai (args as a JSON string)": `{"choices":[{"message":{"tool_calls":[
			{"function":{"name":"play_card","arguments":"{\"card\":2}"}}]}}]}`,
		"anthropic (args as an object)": `{"content":[{"type":"text","text":"thinking"},
			{"type":"tool_use","name":"play_card","input":{"card":3}}]}`,
		"quoted number is the same decision": `{"choices":[{"message":{"tool_calls":[
			{"function":{"name":"play_card","arguments":"{\"card\":\"1\"}"}}]}}]}`,
	}
	want := map[string]int{
		"openai (args as a JSON string)":     2,
		"anthropic (args as an object)":      3,
		"quoted number is the same decision": 1,
	}
	for name, body := range shapes {
		t.Run(name, func(t *testing.T) {
			srv := fakeModel(t, 200, body)
			defer srv.Close()
			got, err := decide(t, srv)
			if err != nil {
				t.Fatalf("decide: %v", err)
			}
			if got != want[name] {
				t.Fatalf("read card %d, want %d", got, want[name])
			}
		})
	}
}

// TestUnusableAnswersFailRatherThanGuess.
//
// Prose, a wrong tool, a fractional card, or an error status must all return an error so the
// caller applies the engine's deterministic fallback. Guessing a card would invent a move the
// model never made; retrying would measure our patience instead of the model.
func TestUnusableAnswersFailRatherThanGuess(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"prose only":      {200, `{"choices":[{"message":{"content":"I play the 2."}}]}`},
		"wrong tool":      {200, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"other","arguments":"{\"card\":2}"}}]}}]}`},
		"fractional card": {200, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"play_card","arguments":"{\"card\":2.5}"}}]}}]}`},
		"no card arg":     {200, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"play_card","arguments":"{}"}}]}}]}`},
		"provider 429":    {429, `{"error":"rate limited"}`},
		"provider 500":    {500, `oops`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := fakeModel(t, c.status, c.body)
			defer srv.Close()
			if _, err := decide(t, srv); err == nil {
				t.Fatal("an unusable answer was accepted; the agent guessed a card")
			}
		})
	}
}

// TestRefusesToRunWithoutAnIdentifiedTarget. A certificate must say what it measured.
func TestRefusesToRunWithoutAnIdentifiedTarget(t *testing.T) {
	spec := ladder.DefaultSpec()
	if _, err := NewLLMSource(Model{Model: "m"}).Decider(context.Background(), "ag", spec); err == nil {
		t.Fatal("ran against an unspecified endpoint")
	}
	if _, err := NewLLMSource(Model{BaseURL: "http://x"}).Decider(context.Background(), "ag", spec); err == nil {
		t.Fatal("ran against an unnamed model")
	}
}
