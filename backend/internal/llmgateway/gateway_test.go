package llmgateway

import (
	"context"
	"github.com/agent-arena/arena/internal/turnproof"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-arena/arena/internal/platform/telemetry"
)

type captureEmitter struct{ events []telemetry.Event }

func (c *captureEmitter) Enabled() bool               { return true }
func (c *captureEmitter) EmitEvent(e telemetry.Event) { c.events = append(c.events, e) }

const openaiBody = `{"id":"chatcmpl-1","model":"gpt-4o-2024-08-06","choices":[{"message":{"content":"hi"}}],` +
	`"usage":{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500,` +
	`"prompt_tokens_details":{"cached_tokens":200},"completion_tokens_details":{"reasoning_tokens":40}}}`

const anthropicBody = `{"id":"msg_1","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hi"}],` +
	`"usage":{"input_tokens":900,"output_tokens":120,"cache_read_input_tokens":100}}`

func newTestProxy(t *testing.T, upstreamHandler http.HandlerFunc) (*Proxy, *captureEmitter, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(upstreamHandler)
	t.Cleanup(up.Close)
	cap := &captureEmitter{}
	p := New(cap, slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithUpstream("openai", up.URL),
		WithUpstream("anthropic", up.URL),
	)
	return p, cap, up
}

func do(t *testing.T, p *Proxy, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

func TestProxy_ForwardsAndObservesOpenAI(t *testing.T) {
	var gotPath, gotAuth, gotPyyol, gotBody string
	p, cap, _ := newTestProxy(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotPyyol = r.Header.Get("X-Pyyol-Key") // must NOT be forwarded
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, openaiBody)
	})

	rec := do(t, p, "POST", "/openai/v1/chat/completions", `{"model":"gpt-4o","messages":[]}`, map[string]string{
		"Authorization": "Bearer sk-dev-key",
		"Content-Type":  "application/json",
		"X-Pyyol-Key":   "agentA",
		"X-Pyyol-Match": "m42",
		"X-Pyyol-Turn":  "3",
	})

	// Upstream got the forwarded request: path rewritten, dev provider key passed,
	// Pyyol control header stripped.
	if gotPath != "/v1/chat/completions" {
		t.Errorf("upstream path = %q, want /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-dev-key" {
		t.Errorf("upstream Authorization = %q (dev key must be forwarded verbatim)", gotAuth)
	}
	if gotPyyol != "" {
		t.Errorf("X-Pyyol-Key must not be forwarded upstream, got %q", gotPyyol)
	}
	if !strings.Contains(gotBody, `"model":"gpt-4o"`) {
		t.Errorf("request body not forwarded: %q", gotBody)
	}

	// Client got the upstream response verbatim.
	if rec.Code != 200 || rec.Body.String() != openaiBody {
		t.Errorf("response not transparent: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// A server-verified model_call_completed was emitted with the REAL model + cost.
	if len(cap.events) != 1 {
		t.Fatalf("want 1 emitted event, got %d", len(cap.events))
	}
	e := cap.events[0]
	if e.EventType != EventModelCallCompleted {
		t.Errorf("event_type = %q", e.EventType)
	}
	if e.Model != "gpt-4o-2024-08-06" { // the REAL model from the response, not the request's "gpt-4o"
		t.Errorf("model = %q, want the response model", e.Model)
	}
	if e.Provider != "openai" || e.ActorID != "agentA" {
		t.Errorf("attribution wrong: provider=%q actor=%q", e.Provider, e.ActorID)
	}
	if e.RunID != "m42" || e.TraceID != telemetry.MatchTraceID("m42") {
		t.Errorf("match correlation wrong: run=%q trace=%q", e.RunID, e.TraceID)
	}
	if e.PromptTokens != 1000 || e.CompletionTokens != 500 || e.TotalTokens != 1500 {
		t.Errorf("tokens wrong: %+v", e)
	}
	// gpt-4o: 800 full input @2.5 + 200 cached @1.25 + 500 out @10, per 1M
	wantCost := (800*2.50 + 200*1.25 + 500*10.00) / 1_000_000
	if abs(e.EstimatedCost-wantCost) > 1e-9 {
		t.Errorf("cost = %v, want %v", e.EstimatedCost, wantCost)
	}
	// Provenance is structural (a column), not buried in payload_json: meter_source
	// = gateway is the verified signal the backend filters on.
	if e.MeterSource != "gateway" {
		t.Errorf("meter_source = %q, want gateway (structural verified signal)", e.MeterSource)
	}
	if e.CachedTokens != 200 || e.ReasoningTokens != 40 {
		t.Errorf("cached/reasoning not structural: cached=%d reasoning=%d", e.CachedTokens, e.ReasoningTokens)
	}
	if e.PricingVersion == "" || e.Currency != "USD" {
		t.Errorf("pricing_version/currency not set: %q/%q", e.PricingVersion, e.Currency)
	}
}

func TestProxy_ObservesAnthropicShape(t *testing.T) {
	p, cap, _ := newTestProxy(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, anthropicBody)
	})
	rec := do(t, p, "POST", "/anthropic/v1/messages", `{"model":"claude-sonnet-4-5"}`, map[string]string{
		"X-Pyyol-Key": "agentB",
	})
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	if len(cap.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(cap.events))
	}
	e := cap.events[0]
	if e.Provider != "anthropic" || e.Model != "claude-sonnet-4-5" {
		t.Errorf("provider/model = %q/%q", e.Provider, e.Model)
	}
	// 900 uncached + 100 cache reads. Anthropic's input_tokens counts only the uncached
	// remainder, so cache tokens are ADDED to recover billable input — the convention every
	// consumer downstream assumes. Reading it raw made those 100 reads DISPLACE 100
	// full-rate tokens instead of adding to them, which understated the bill.
	if e.PromptTokens != 1000 || e.CompletionTokens != 120 {
		t.Errorf("anthropic tokens wrong: %+v", e)
	}
	// claude-sonnet: 900 input @3 + 100 cache reads @0.30 + 120 out @15
	wantCost := (900*3.00 + 100*0.30 + 120*15.00) / 1_000_000
	if abs(e.EstimatedCost-wantCost) > 1e-9 {
		t.Errorf("cost = %v, want %v", e.EstimatedCost, wantCost)
	}
}

func TestProxy_RequiresPyyolKey(t *testing.T) {
	called := false
	p, cap, _ := newTestProxy(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	rec := do(t, p, "POST", "/openai/v1/chat/completions", `{}`, map[string]string{
		"Authorization": "Bearer sk-dev",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rec.Code)
	}
	if called {
		t.Error("must not reach upstream without a Pyyol key")
	}
	if len(cap.events) != 0 {
		t.Error("must not emit without auth")
	}
}

func TestProxy_UnknownUpstream404(t *testing.T) {
	p, _, _ := newTestProxy(t, func(w http.ResponseWriter, r *http.Request) {})
	rec := do(t, p, "POST", "/gemini/v1/x", `{}`, map[string]string{"X-Pyyol-Key": "a"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

func TestProxy_StreamingPassesThroughNoUsage(t *testing.T) {
	p, cap, _ := newTestProxy(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
	})
	rec := do(t, p, "POST", "/openai/v1/chat/completions", `{"model":"gpt-4o","stream":true}`, map[string]string{
		"X-Pyyol-Key": "agentA",
	})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Errorf("stream not passed through: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if len(cap.events) != 0 {
		t.Errorf("streaming carries no usage; want 0 events, got %d", len(cap.events))
	}
}

func TestProxy_NonSuccessNoEmit(t *testing.T) {
	p, cap, _ := newTestProxy(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	})
	rec := do(t, p, "POST", "/openai/v1/chat/completions", `{}`, map[string]string{"X-Pyyol-Key": "a"})
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("code = %d, want 429 (transparent)", rec.Code)
	}
	if len(cap.events) != 0 {
		t.Errorf("error responses carry no billable usage; want 0 events, got %d", len(cap.events))
	}
}

func TestProxy_VerifiedHookFires(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, openaiBody)
	}))
	t.Cleanup(up.Close)

	var got []VerifiedCall
	// em=nil (Lens disabled) to prove the hook is independent of telemetry.
	p := New(nil, nil, WithUpstream("openai", up.URL), WithVerifiedHook(func(_ context.Context, c VerifiedCall) {
		got = append(got, c)
	}))
	rec := do(t, p, "POST", "/openai/v1/chat/completions", `{"model":"gpt-4o"}`, map[string]string{
		"X-Pyyol-Key":   "agentZ",
		"X-Pyyol-Match": "m99",
	})
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	if len(got) != 1 || got[0].AgentID != "agentZ" || got[0].MatchID != "m99" || got[0].CostUSD <= 0 {
		t.Fatalf("verified hook = %+v, want agent=agentZ match=m99 cost>0", got)
	}
	// The model attribution the board ranks on: read out of the UPSTREAM response, not
	// out of the request the agent sent. openaiBody names gpt-4o-2024-08-06 while the
	// request asked for "gpt-4o", so this also proves we report what actually served
	// the call rather than what was requested.
	if got[0].Provider != "openai" {
		t.Errorf("provider = %q, want openai", got[0].Provider)
	}
	if got[0].Model == "" || got[0].Model == "gpt-4o" {
		t.Errorf("model = %q, want the model named in the upstream response body", got[0].Model)
	}
	if got[0].TotalTokens <= 0 || got[0].PromptTokens <= 0 || got[0].CompletionTokens <= 0 {
		t.Errorf("token usage = %+v, want the provider-reported split", got[0])
	}
}

func TestProxy_VerifiedHookNotFiredOnError(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	}))
	t.Cleanup(up.Close)
	fired := false
	p := New(nil, nil, WithUpstream("openai", up.URL), WithVerifiedHook(func(_ context.Context, _ VerifiedCall) { fired = true }))
	do(t, p, "POST", "/openai/v1/chat/completions", `{}`, map[string]string{"X-Pyyol-Key": "a"})
	if fired {
		t.Error("verified hook must not fire on a non-2xx response")
	}
}

func TestExtractUsage_NoUsageBody(t *testing.T) {
	if _, ok := extractUsage([]byte(`{"error":"nope"}`)); ok {
		t.Error("body without usage should return ok=false")
	}
	if _, ok := extractUsage([]byte(`not json`)); ok {
		t.Error("invalid json should return ok=false")
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// A call carrying a valid proof token is BOUND to that decision; one without is not.
// This is the line between "an LLM call happened somewhere" and "this decision was
// made by an LLM", and it is the whole basis of ranked integrity.
func TestVerifiedHookReportsWhetherTheCallIsBound(t *testing.T) {
	sig := turnproof.New("test-secret")

	type got struct {
		match string
		round int
		bound bool
	}
	var seen []got
	hook := func(_ context.Context, c VerifiedCall) {
		seen = append(seen, got{c.MatchID, c.Round, c.Bound})
	}

	p := New(nil, slog.Default(), WithVerifiedHook(hook), WithTurnVerifier(sig))

	body := []byte(`{"model":"gpt-4o","usage":{"prompt_tokens":10,"completion_tokens":5}}`)

	// Genuine: the platform issued this token for (ag_1, m_1, round 4).
	h := http.Header{}
	h.Set("X-Pyyol-Match", "m_1")
	h.Set("X-Pyyol-Turn", "4")
	h.Set("X-Pyyol-Proof", sig.Mint("ag_1", "m_1", 4))
	p.observe("openai", "ag_1", h, 12, body)

	// Forged: the agent claims round 9 with round 4's token — the cheap trick this
	// exists to stop, since otherwise one call could cover a whole match.
	h2 := http.Header{}
	h2.Set("X-Pyyol-Match", "m_1")
	h2.Set("X-Pyyol-Turn", "9")
	h2.Set("X-Pyyol-Proof", sig.Mint("ag_1", "m_1", 4))
	p.observe("openai", "ag_1", h2, 12, body)

	// No proof at all — a legitimate batching/warm-up call. Observed, not counted.
	h3 := http.Header{}
	h3.Set("X-Pyyol-Match", "m_1")
	h3.Set("X-Pyyol-Turn", "5")
	p.observe("openai", "ag_1", h3, 12, body)

	if len(seen) != 3 {
		t.Fatalf("hook fired %d times, want 3 — every observed call must still report", len(seen))
	}
	if !seen[0].bound || seen[0].round != 4 {
		t.Fatalf("a genuine proof was not counted: %+v", seen[0])
	}
	if seen[1].bound {
		t.Fatal("a token replayed onto another round was accepted")
	}
	if seen[2].bound {
		t.Fatal("a call with no proof was counted as bound")
	}
}

// With no verifier configured nothing is bound, but calls must still be observed and
// billed — disabling the proof must not silently stop metering.
func TestWithoutAVerifierNothingIsBoundButCallsStillReport(t *testing.T) {
	var fired int
	var bound bool
	p := New(nil, slog.Default(), WithVerifiedHook(
		func(_ context.Context, c VerifiedCall) { fired++; bound = bound || c.Bound }))

	h := http.Header{}
	h.Set("X-Pyyol-Match", "m_1")
	h.Set("X-Pyyol-Turn", "1")
	h.Set("X-Pyyol-Proof", "anything")
	p.observe("openai", "ag_1", h, 5, []byte(`{"model":"gpt-4o","usage":{"prompt_tokens":1,"completion_tokens":1}}`))

	if fired != 1 {
		t.Fatalf("call was not observed: fired=%d", fired)
	}
	if bound {
		t.Fatal("bound reported true with no verifier — must fail closed")
	}
}
