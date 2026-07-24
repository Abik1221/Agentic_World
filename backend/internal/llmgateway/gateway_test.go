package llmgateway

import (
	"context"
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
	if v, _ := e.PayloadJSON["verified"].(bool); !v {
		t.Error("gateway event must be marked verified")
	}
	if src, _ := e.PayloadJSON["source"].(string); src != "gateway" {
		t.Errorf("source = %q, want gateway", src)
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
	if e.PromptTokens != 900 || e.CompletionTokens != 120 {
		t.Errorf("anthropic tokens wrong: %+v", e)
	}
	// claude-sonnet: 800 input @3 + 100 cached @0.30 + 120 out @15
	wantCost := (800*3.00 + 100*0.30 + 120*15.00) / 1_000_000
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

	var got []string
	// em=nil (Lens disabled) to prove the badge hook is independent of telemetry.
	p := New(nil, nil, WithUpstream("openai", up.URL), WithVerifiedHook(func(_ context.Context, agentID string) {
		got = append(got, agentID)
	}))
	rec := do(t, p, "POST", "/openai/v1/chat/completions", `{"model":"gpt-4o"}`, map[string]string{"X-Pyyol-Key": "agentZ"})
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	if len(got) != 1 || got[0] != "agentZ" {
		t.Errorf("verified hook = %v, want [agentZ]", got)
	}
}

func TestProxy_VerifiedHookNotFiredOnError(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	}))
	t.Cleanup(up.Close)
	fired := false
	p := New(nil, nil, WithUpstream("openai", up.URL), WithVerifiedHook(func(_ context.Context, _ string) { fired = true }))
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
