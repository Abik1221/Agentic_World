package llmgw

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/agent-arena/arena/internal/movebind"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/turnproof"
)

// Completion binding at the gateway: does the platform learn what the MODEL answered?
//
// The gateway is the only party that sees both the completion and, later, the submitted move.
// If it does not extract the move here, nothing downstream can enforce anything — so these
// tests are the difference between the whole Phase-1 mechanism working and it being inert.

// A goofspiel match id, so ToolFor resolves to play_card. The game comes from the id prefix
// (telemetry.GameFromMatchID), which is why a bare "m_1" is a Goofspiel match.
const bindMatch = "m_bind1"

func bindingUpstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

// The ordinary path: a model emits play_card(7) and the platform records "card:7" against
// that exact turn, with a receipt it can later re-verify.
func TestGatewayBindsTheMoveTheModelProduced(t *testing.T) {
	const secret, agent, round = "s3cret", "ag_bind", 4
	up := bindingUpstream(t, `{"model":"gpt-5.2","choices":[{"message":{"tool_calls":[
		{"function":{"name":"play_card","arguments":"{\"card\":7}"}}]}}],
		"usage":{"prompt_tokens":100,"completion_tokens":20}}`)
	g, rec := gwFor(t, up, secret)

	proof := turnproof.New(secret).Mint(agent, bindMatch, round)
	post(g, agent, bindMatch, fmt.Sprint(round), proof, `{"model":"gpt-5.2"}`)
	rec.settle(t, 1)

	binds := rec.bindRecords()
	if len(binds) != 1 {
		t.Fatalf("BindDecision called %d times, want 1", len(binds))
	}
	b := binds[0]
	if b.move != "card:7" {
		t.Fatalf("bound move = %q, want %q — the platform did not learn what the model answered",
			b.move, "card:7")
	}
	if len(b.completionHash) != 64 {
		t.Fatalf("completion hash = %q, want a 64-char SHA-256", b.completionHash)
	}
	if b.receipt == "" {
		t.Fatal("no receipt minted, so the stored move is an unattested mutable column")
	}
	// The receipt must actually verify against what was stored. If it does not, a later
	// audit or replay would read a legitimate row as tampered.
	if !turnproof.New(secret).VerifyDecision(agent, bindMatch, round, b.completionHash, b.move, b.receipt) {
		t.Fatal("the minted receipt does not verify against the values it was minted over")
	}
}

// THE forgery property, and the one that makes this safe to enforce.
//
// On an unbound call the match and round are unverified headers the AGENT chose. If the
// gateway recorded an extracted move against them, an agent could make one unproven call
// carrying a tool call of its choosing and WRITE a move into any turn's slot — then submit
// that move and pass the check. The anti-cheat control would have become the cheat.
func TestGatewayBindsNoMoveOnAnUnprovenCall(t *testing.T) {
	const secret, agent, round = "s3cret", "ag_forge", 4
	up := bindingUpstream(t, `{"model":"gpt-5.2","choices":[{"message":{"tool_calls":[
		{"function":{"name":"play_card","arguments":"{\"card\":7}"}}]}}]}`)
	g, rec := gwFor(t, up, secret)

	// A token for a DIFFERENT turn — the closest thing an agent can actually obtain.
	stolen := turnproof.New(secret).Mint(agent, bindMatch, 1)
	post(g, agent, bindMatch, fmt.Sprint(round), stolen, `{"model":"gpt-5.2"}`)
	calls, _ := rec.settle(t, 1)

	if calls[0].Bound {
		t.Fatal("a token minted for another round verified — the turn proof is broken")
	}
	if calls[0].ExtractedMove != "" {
		t.Fatalf("extracted a move (%q) from an UNPROVEN call: an agent could write a move "+
			"into any turn it names", calls[0].ExtractedMove)
	}
	if binds := rec.bindRecords(); len(binds) != 0 {
		t.Fatalf("BindDecision called %d times for an unproven call", len(binds))
	}
}

// A completion with no move tool call must bind NOTHING rather than something empty. This is
// the case that keeps enforcement inert for every agent that has not adopted the contract,
// and getting it wrong would reject their honest play wholesale.
func TestGatewayBindsNoMoveWhenTheModelDidNotCallTheTool(t *testing.T) {
	const secret, agent, round = "s3cret", "ag_prose", 4
	up := bindingUpstream(t, `{"model":"gpt-5.2","choices":[{"message":{"content":"I'll play the 7."}}],
		"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	g, rec := gwFor(t, up, secret)

	proof := turnproof.New(secret).Mint(agent, bindMatch, round)
	post(g, agent, bindMatch, fmt.Sprint(round), proof, `{"model":"gpt-5.2"}`)
	calls, _ := rec.settle(t, 1)

	if !calls[0].Bound {
		t.Fatal("a validly proven call was not bound")
	}
	if calls[0].ExtractedMove != "" {
		t.Fatalf("bound %q from prose — the platform must not guess a move out of free text",
			calls[0].ExtractedMove)
	}
	// Still a bound DECISION: the call is proven LLM-backed, which is what coverage counts.
	binds := rec.bindRecords()
	if len(binds) != 1 || binds[0].move != "" {
		t.Fatalf("want one bound decision carrying no move, got %+v", binds)
	}
}

// A move in a different game's tool must not bind. Otherwise an agent in a Mafia match could
// bind a Goofspiel-shaped move, or vice versa.
func TestGatewayIgnoresAnotherGamesTool(t *testing.T) {
	const secret, agent, round = "s3cret", "ag_wrongtool", 4
	up := bindingUpstream(t, `{"model":"gpt-5.2","choices":[{"message":{"tool_calls":[
		{"function":{"name":"mafia_action","arguments":"{\"kind\":\"kill\",\"target\":3}"}}]}}]}`)
	g, rec := gwFor(t, up, secret)

	// bindMatch is a GOOFSPIEL match, so only play_card is the move tool here.
	proof := turnproof.New(secret).Mint(agent, bindMatch, round)
	post(g, agent, bindMatch, fmt.Sprint(round), proof, `{"model":"gpt-5.2"}`)
	calls, _ := rec.settle(t, 1)

	if calls[0].ExtractedMove != "" {
		t.Fatalf("bound %q from another game's tool", calls[0].ExtractedMove)
	}
}

// --- Streaming ----------------------------------------------------------------------------

func sseUpstream(t *testing.T, frames ...string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range frames {
			_, _ = w.Write([]byte("data: " + f + "\n\n"))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// Streamed responses were previously never captured at all, so a streaming agent could
// neither be costed nor bound. Both are asserted here because they were one omission.
func TestGatewayBindsAndCostsAStreamedCall(t *testing.T) {
	const secret, agent, round = "s3cret", "ag_stream", 6
	up := sseUpstream(t,
		`{"type":"message_start","message":{"model":"claude-opus-4","usage":{"input_tokens":400,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"play_card"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"card\""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":": 11}"}}`,
		`{"type":"message_delta","usage":{"output_tokens":90}}`,
	)
	g, rec := gwFor(t, up, secret)

	proof := turnproof.New(secret).Mint(agent, bindMatch, round)
	// stream:true in the request body is what marks the call streamed.
	post(g, agent, bindMatch, fmt.Sprint(round), proof, `{"model":"claude-opus-4","stream":true}`)
	calls, _ := rec.settle(t, 1)
	c := calls[0]

	if !c.Streamed {
		t.Fatal("call was not recognised as streamed")
	}
	if c.ExtractedMove != "card:11" {
		t.Fatalf("streamed bound move = %q, want card:11 — a streaming agent cannot be bound",
			c.ExtractedMove)
	}
	// Usage, which used to be zero for every streamed call on the platform.
	if c.CompletionTokens != 90 {
		t.Fatalf("streamed completion tokens = %d, want 90", c.CompletionTokens)
	}
	if c.CachedReadTokens != 1000 || c.CachedWriteTokens != 200 {
		t.Fatalf("streamed cache tokens = read %d / write %d, want 1000 / 200",
			c.CachedReadTokens, c.CachedWriteTokens)
	}
	// Anthropic reports cache counts ALONGSIDE input_tokens, so the normalized prompt total
	// is 400 + 1000 + 200. Asserted because the streamed path must use the SAME normalizer as
	// the non-streamed one, or verified and self-reported cost diverge for one call.
	if c.PromptTokens != 1600 {
		t.Fatalf("streamed prompt tokens = %d, want 1600 (400 + 1000 read + 200 write)", c.PromptTokens)
	}
	if c.CostUSD <= 0 {
		t.Fatal("a streamed call priced at zero: verified spend would understate every streaming agent")
	}
}

// A streamed response must still reach the client byte-for-byte. The tee exists precisely so
// that observing the stream costs the agent nothing.
func TestGatewayStreamsThroughUnmodifiedWhileBinding(t *testing.T) {
	const secret, agent, round = "s3cret", "ag_passthru", 2
	frames := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"play_card"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"card\": 5}"}}`,
	}
	up := sseUpstream(t, frames...)
	g, rec := gwFor(t, up, secret)

	proof := turnproof.New(secret).Mint(agent, bindMatch, round)
	w := post(g, agent, bindMatch, fmt.Sprint(round), proof, `{"model":"claude-opus-4","stream":true}`)

	got := w.Body.String()
	for _, f := range frames {
		if !strings.Contains(got, f) {
			t.Fatalf("frame missing from the agent's response: %s", f)
		}
	}
	calls, _ := rec.settle(t, 1)
	if calls[0].ExtractedMove != "card:5" {
		t.Fatalf("bound %q, want card:5", calls[0].ExtractedMove)
	}
}

// An oversized response must not be bound from a partial capture. Half a tool call can decode
// to a DIFFERENT move than the model chose, and that would reject an honest turn.
func TestGatewayBindsNothingWhenTheCaptureIsTruncated(t *testing.T) {
	const secret, agent, round = "s3cret", "ag_big", 3
	// Padding pushes the body past maxCapturedBytes; the tool call sits at the end so a
	// truncated capture would miss it entirely, and a naive parser would see nothing.
	padding := strings.Repeat("x", maxCapturedBytes+1024)
	up := bindingUpstream(t, `{"model":"gpt-5.2","pad":"`+padding+`","choices":[{"message":{"tool_calls":[
		{"function":{"name":"play_card","arguments":"{\"card\":7}"}}]}}]}`)
	g, rec := gwFor(t, up, secret)

	proof := turnproof.New(secret).Mint(agent, bindMatch, round)
	post(g, agent, bindMatch, fmt.Sprint(round), proof, `{"model":"gpt-5.2"}`)
	calls, _ := rec.settle(t, 1)

	if calls[0].ExtractedMove != "" {
		t.Fatalf("bound %q from a truncated capture", calls[0].ExtractedMove)
	}
}

// The capture is bounded, but the AGENT's copy is not. Rule 1: the gateway must never be why
// a call fails, and truncating a developer's response would be exactly that.
func TestGatewayDeliversAnOversizedResponseInFull(t *testing.T) {
	const secret, agent, round = "s3cret", "ag_big2", 3
	body := `{"model":"gpt-5.2","pad":"` + strings.Repeat("y", maxCapturedBytes+2048) + `"}`
	up := bindingUpstream(t, body)
	g, _ := gwFor(t, up, secret)

	proof := turnproof.New(secret).Mint(agent, bindMatch, round)
	w := post(g, agent, bindMatch, fmt.Sprint(round), proof, `{"model":"gpt-5.2"}`)
	if w.Body.Len() != len(body) {
		t.Fatalf("agent received %d bytes of a %d-byte response — the tee truncated the stream",
			w.Body.Len(), len(body))
	}
}

// A game the platform has no move contract for must bind nothing rather than guess.
func TestGatewayBindsNothingForAnUnknownGame(t *testing.T) {
	const secret, agent, round = "s3cret", "ag_unknown", 1
	up := bindingUpstream(t, `{"choices":[{"message":{"tool_calls":[
		{"function":{"name":"play_card","arguments":"{\"card\":7}"}}]}}]}`)
	g, rec := gwFor(t, up, secret)

	const noGame = "zz_not_a_match_prefix"
	if movebind.ToolFor("") != "" {
		t.Fatal("an unknown game resolved to a move tool")
	}
	proof := turnproof.New(secret).Mint(agent, noGame, round)
	post(g, agent, noGame, fmt.Sprint(round), proof, `{"model":"gpt-5.2"}`)
	calls, _ := rec.settle(t, 1)

	if calls[0].ExtractedMove != "" {
		t.Fatalf("bound %q for a match id that names no known game", calls[0].ExtractedMove)
	}
}

// --- Traceability ---------------------------------------------------------------------------

// The Lens span for a bound call must carry the extracted move.
//
// This is the field a developer opens the trace to see when a turn is rejected: without it,
// "your move did not match the model's output" is an assertion they cannot check against
// anything. It is also the only place the two values appear side by side, since the submitted
// move lives on the match and the extracted one on the gateway.
//
// Asserted alongside the economics because they ship in the same event and are read by the same
// consumers — a span that carried the move but lost meter_source would drop out of verified cost
// entirely, which is the failure mode the comment in emit() was written for.
func TestGatewaySpanCarriesTheExtractedMoveAndEconomics(t *testing.T) {
	const secret, agent, round = "s3cret", "ag_span", 5
	up := bindingUpstream(t, `{"model":"claude-opus-4","content":[
		{"type":"tool_use","name":"play_card","input":{"card":9}}],
		"usage":{"input_tokens":400,"output_tokens":80,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200}}`)
	g, rec := gwFor(t, up, secret)
	em := &captureEmitter{on: true}
	g.SetEmitter(em)

	proof := turnproof.New(secret).Mint(agent, bindMatch, round)
	post(g, agent, bindMatch, fmt.Sprint(round), proof, `{"model":"claude-opus-4"}`)
	rec.settle(t, 1)

	if len(em.events) != 1 {
		t.Fatalf("emitted %d spans, want 1", len(em.events))
	}
	ev := em.events[0]

	move, _ := ev.PayloadJSON["extracted_move"].(string)
	if move != "card:9" {
		t.Fatalf("span extracted_move = %q, want %q — a rejected turn would be unexplainable "+
			"from the trace", move, "card:9")
	}
	if bound, _ := ev.PayloadJSON["turn_bound"].(bool); !bound {
		t.Fatal("span turn_bound is false for a proven call")
	}
	if ev.PayloadJSON["cache_write_tokens"] != 200 {
		t.Fatalf("span cache_write_tokens = %v, want 200", ev.PayloadJSON["cache_write_tokens"])
	}
	// MeterSource is the STRUCTURAL verified signal: the backend filters verified economics on
	// that column alone, so an event without it contributes nothing however good its provenance.
	if ev.MeterSource != telemetry.MeterSourceGateway {
		t.Fatalf("span MeterSource = %q, want %q", ev.MeterSource, telemetry.MeterSourceGateway)
	}
	// SessionID is the boards' (agent, game) join key. Without it verified gateway cost silently
	// contributed $0 to every board — the events existed and joined to nothing.
	if ev.SessionID != "goofspiel" {
		t.Fatalf("span SessionID = %q, want the arena %q", ev.SessionID, "goofspiel")
	}
	if ev.RunID != bindMatch {
		t.Fatalf("span RunID = %q, want the match %q", ev.RunID, bindMatch)
	}
	// Anthropic reports cache counts alongside input_tokens, so the normalized prompt total is
	// 400 + 1000 + 200. The span must agree with the recorded call, not re-derive it.
	if ev.PromptTokens != 1600 {
		t.Fatalf("span PromptTokens = %d, want 1600", ev.PromptTokens)
	}
	if ev.EstimatedCost <= 0 {
		t.Fatal("span carries no cost, so the trace and the boards would disagree")
	}
}

// An UNBOUND call must not put a move in the trace either.
//
// The trace is where a dispute is settled, so a move attributed to a turn the platform could not
// verify would be evidence of something that was never established.
func TestGatewaySpanCarriesNoMoveWhenNothingWasBound(t *testing.T) {
	const secret, agent, round = "s3cret", "ag_span2", 5
	up := bindingUpstream(t, `{"model":"claude-opus-4","content":[
		{"type":"tool_use","name":"play_card","input":{"card":9}}]}`)
	g, rec := gwFor(t, up, secret)
	em := &captureEmitter{on: true}
	g.SetEmitter(em)

	// A proof minted for a DIFFERENT round: the call is real, but it is not proven to be this
	// turn's, so nothing about it may be attributed to this turn.
	stolen := turnproof.New(secret).Mint(agent, bindMatch, round+1)
	post(g, agent, bindMatch, fmt.Sprint(round), stolen, `{"model":"claude-opus-4"}`)
	rec.settle(t, 1)

	if len(em.events) != 1 {
		t.Fatalf("emitted %d spans, want 1", len(em.events))
	}
	ev := em.events[0]
	if move, _ := ev.PayloadJSON["extracted_move"].(string); move != "" {
		t.Fatalf("span carries extracted_move %q for an UNPROVEN call", move)
	}
	if bound, _ := ev.PayloadJSON["turn_bound"].(bool); bound {
		t.Fatal("span reports turn_bound for a call whose proof did not verify")
	}
}

// A span is bound in EVERY game, not just the one the lab drives.
//
// The gateway resolves the game from the match id prefix and dispatches through
// movebind.ToolFor / CanonPlan, so nothing about range binding is Goofspiel-specific. But the
// only end-to-end evidence is a Goofspiel match, and "it dispatches by game" is exactly the
// kind of claim that turns out to have a hardcoded constant behind it. Mafia and Monopoly are
// the two with more seats and more money on the table.
func TestASpanBindsEveryRoundInMafia(t *testing.T) {
	cases := []struct {
		name, matchID, tool, body string
		wantMoves                 []string
	}{
		{
			name:    "mafia",
			matchID: "mf_spanitest0001",
			tool:    "mafia_action",
			// Seat 0 is a real player and an ABSENT target is not seat 0 — the span must keep
			// those distinct exactly as a single call does.
			body: `{"model":"m","content":[{"type":"tool_use","name":"mafia_action","input":{"plan":[
				{"round":4,"kind":"kill","target":0},
				{"round":5,"kind":"abstain"}]}}]}`,
			wantMoves: []string{"kill:0", "abstain:none"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := jsonUpstream(t, tc.body)
			g, rec := gwFor(t, up, "s")
			proof := turnproof.New("s").Mint("ag_1", tc.matchID, 4)

			if rr := post(g, "ag_1", tc.matchID, "4", proof, `{"model":"m"}`); rr.Code != 200 {
				t.Fatalf("code = %d, want 200", rr.Code)
			}
			rec.settle(t, 1)

			var got []bindRecord
			for _, b := range rec.bindRecords() {
				if b.matchID == tc.matchID {
					got = append(got, b)
				}
			}
			if len(got) != len(tc.wantMoves) {
				t.Fatalf("bound %d rounds %+v, want %d — the span covered fewer rounds than the "+
					"completion decided, so this game is not getting range binding at all",
					len(got), got, len(tc.wantMoves))
			}
			sort.Slice(got, func(i, j int) bool { return got[i].round < got[j].round })
			for i, want := range tc.wantMoves {
				if got[i].move != want {
					t.Errorf("round %d bound %q, want %q", got[i].round, got[i].move, want)
				}
				if got[i].round != 4+i {
					t.Errorf("bound round %d, want %d", got[i].round, 4+i)
				}
				if got[i].receipt == "" {
					t.Errorf("round %d has no receipt — an unattested row is one anything with "+
						"database access can rewrite", got[i].round)
				}
			}
			// ONE completion, so one hash across the whole span. That shared hash is what ties
			// the rounds back together as a single call when a developer disputes one of them.
			if got[0].completionHash == "" || got[0].completionHash != got[1].completionHash {
				t.Errorf("completion hashes %q / %q — every round in a span must carry the hash of "+
					"the ONE response it came from", got[0].completionHash, got[1].completionHash)
			}
		})
	}
}
