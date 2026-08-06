package sandbox

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/match"
)

// viewFromJSON builds a match.AgentView from JSON.
//
// The nested view structs (sideView, legalView, …) are unexported, so a test in this
// package cannot set `You.Hand` or `LegalActions.PlayCardFrom` with a literal. The
// fields themselves are exported and JSON-tagged, so decoding is the supported way to
// construct a realistic view without widening the match package's API for tests.
func viewFromJSON(t *testing.T, doc string) match.AgentView {
	t.Helper()
	var v match.AgentView
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatalf("build view: %v", err)
	}
	return v
}

// scriptedDriver replays a fixed sequence of views, one per State call, and records
// the cards submitted through Act. The last view is repeated if the loop asks again.
type scriptedDriver struct {
	mu    sync.Mutex
	views []match.AgentView
	calls int
	acted []int
}

func (d *scriptedDriver) State(context.Context, string, string, bool, time.Duration) (match.AgentView, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := d.calls
	if i >= len(d.views) {
		i = len(d.views) - 1
	}
	d.calls++
	return d.views[i], nil
}

func (d *scriptedDriver) Act(_ context.Context, _, _ string, _, card int, _ string) (match.AgentView, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.acted = append(d.acted, card)
	return match.AgentView{}, nil
}

// TestPushPlay_RecordsInputView is the regression guard for a NULL input_json.
//
// The sandbox loop recorded a decision's answer (action, outcome, latency, rationale)
// but not the question, because the benchmark.Decision it built omitted the View field
// that its three sibling push paths all set. Every sandbox match therefore persisted
// agent_match_decisions.input_json = NULL, and the developer's decision inspector could
// only ever say "no view was recorded for this move".
//
// Asserting on the flushed benchmark payload rather than on turnView() is deliberate: a
// unit test on the view builder passes just as happily when the caller forgets to record
// it, which is precisely the bug that shipped.
func TestPushPlay_RecordsInputView(t *testing.T) {
	// The agent endpoint: answers every turn with a legal card.
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"card":7,"rationale":"mid card holds the tail","usage":{"prompt_tokens":10,"completion_tokens":5,"model":"claude-opus-4","provider":"Anthropic"}}`))
	}))
	defer agent.Close()

	active := viewFromJSON(t, `{
		"status":"active","round":1,"total_rounds":13,"current_prize":9,"prize_pool":9,
		"your_turn":true,
		"you":{"hand":[3,7,11],"score":4},
		"opponent":{"hand":[2,5,9],"score":6},
		"legal_actions":{"play_card_from":[3,7,11]},
		"history":[{"round":0,"prize":5,"prize_pool":5,"your_card":1,"opp_card":2,"winner":"opponent"}]
	}`)
	finished := viewFromJSON(t, `{"status":"finished","result":{"winner":"opponent","your_score":4,"opp_score":6}}`)

	var (
		mu      sync.Mutex
		payload []byte
	)
	p := &pushPlayer{
		driver: &scriptedDriver{views: []match.AgentView{active, finished}},
		client: agentclient.New(agentclient.Config{
			Timeout: 5 * time.Second, MaxTimeout: 5 * time.Second, AllowPrivate: true,
		}),
		log:      slog.New(slog.DiscardHandler),
		maxMatch: 20 * time.Second,
		persist: func(_ context.Context, _ string, b []byte) error {
			mu.Lock()
			defer mu.Unlock()
			payload = b
			return nil
		},
	}

	p.drive("m_input_1", "ag_test", agentclient.Target{EndpointURL: agent.URL, Token: "secret"})

	mu.Lock()
	got := payload
	mu.Unlock()
	if len(got) == 0 {
		t.Fatal("no benchmark payload was flushed")
	}

	// The payload shape belongs to internal/benchmark; decode only what this test asserts
	// so a additive schema change here is not a false failure.
	var flushed struct {
		Seats []struct {
			DecisionLog []struct {
				Action         string          `json:"action"`
				Input          json.RawMessage `json:"input"`
				InputTruncated bool            `json:"input_truncated"`
			} `json:"decision_log"`
		} `json:"seats"`
	}
	if err := json.Unmarshal(got, &flushed); err != nil {
		t.Fatalf("decode benchmark payload: %v\n%s", err, got)
	}

	var decisions int
	for _, s := range flushed.Seats {
		for _, d := range s.DecisionLog {
			decisions++
			if d.InputTruncated {
				t.Errorf("action %q: input reported truncated; the view is small and should be kept whole", d.Action)
			}
			if len(d.Input) == 0 {
				t.Fatalf("action %q: input view was not recorded — agent_match_decisions.input_json "+
					"will be NULL and the decision inspector cannot show what the agent was handed", d.Action)
			}
			// The recorded input must be the view actually POSTed, not a placeholder:
			// spot-check the fields that make the move judgeable.
			var in struct {
				Round        int   `json:"round"`
				CurrentPrize int   `json:"current_prize"`
				YourHand     []int `json:"your_hand"`
				LegalActions []int `json:"legal_actions"`
			}
			if err := json.Unmarshal(d.Input, &in); err != nil {
				t.Fatalf("recorded input is not valid JSON: %v", err)
			}
			if in.CurrentPrize != 9 {
				t.Errorf("recorded input current_prize=%d want 9", in.CurrentPrize)
			}
			if len(in.YourHand) != 3 {
				t.Errorf("recorded input your_hand=%v want the seat's 3 cards", in.YourHand)
			}
			if len(in.LegalActions) != 3 {
				t.Errorf("recorded input legal_actions=%v want the 3 legal cards", in.LegalActions)
			}
		}
	}
	if decisions == 0 {
		t.Fatal("the loop recorded no decisions, so the input assertion never ran")
	}
}
