package llmgw

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/benchmark"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/turnproof"
)

type fakeRecorder struct {
	mu    sync.Mutex
	calls []Call
	bound []string
	costs []VerifiedCost
	binds []bindRecord
}

func (f *fakeRecorder) RecordCall(_ context.Context, c Call) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	return nil
}
func (f *fakeRecorder) BindDecision(_ context.Context, matchID, agent string, round int, move, completionHash, receipt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bound = append(f.bound, matchID+"|"+agent+"|"+itoa(round))
	f.binds = append(f.binds, bindRecord{
		matchID: matchID, agent: agent, round: round,
		move: move, completionHash: completionHash, receipt: receipt,
	})
	return nil
}

// bindRecord is what BindDecision was actually told, so a test can assert on the COMPLETION
// BINDING and not merely on the fact that some binding happened.
type bindRecord struct {
	matchID, agent       string
	round                int
	move, completionHash string
	receipt              string
}

func (f *fakeRecorder) bindRecords() []bindRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bindRecord(nil), f.binds...)
}
func (f *fakeRecorder) RecordVerifiedCost(_ context.Context, c VerifiedCost) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.costs = append(f.costs, c)
	return nil
}
func (f *fakeRecorder) verifiedCosts() []VerifiedCost {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]VerifiedCost(nil), f.costs...)
}
func (f *fakeRecorder) snapshot() ([]Call, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...), append([]string(nil), f.bound...)
}
func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// settle waits for the detached recording goroutine. Recording is deliberately off the
// response path, so a test that asserts immediately would race it.
func (f *fakeRecorder) settle(t *testing.T, wantCalls int) ([]Call, []string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if c, _ := f.snapshot(); len(c) >= wantCalls {
			time.Sleep(20 * time.Millisecond) // let BindDecision land too
			return f.snapshot()
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, _ := f.snapshot()
	t.Fatalf("recorder saw %d calls, wanted %d", len(c), wantCalls)
	return nil, nil
}

func gwFor(t *testing.T, upstream *httptest.Server, secret string) (*Gateway, *fakeRecorder) {
	t.Helper()
	rec := &fakeRecorder{}
	g := New(Config{Upstreams: map[string]string{"openai": upstream.URL, "anthropic": upstream.URL}},
		rec, turnproof.New(secret), slog.New(slog.DiscardHandler))
	return g, rec
}

func jsonUpstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Saw-Auth", r.Header.Get("Authorization"))
		w.Header().Set("X-Upstream-Saw-Pyyol", r.Header.Get(HeaderProof))
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func post(g *Gateway, agent, match, round, proof, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/gw/openai/v1/chat/completions",
		strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer sk-developers-own-key")
	if match != "" {
		r.Header.Set(HeaderMatch, match)
	}
	if round != "" {
		r.Header.Set(HeaderTurn, round)
	}
	if proof != "" {
		r.Header.Set(HeaderProof, proof)
	}
	w := httptest.NewRecorder()
	g.Proxy(w, r, agent, "openai", "/v1/chat/completions")
	return w
}

// ── THE anti-cheat property ─────────────────────────────────────────────────

// A call is CREDITED only when its turn proof verifies for exactly the decision it claims.
//
// This is the whole point of the gateway. Routing alone proves a call happened somewhere;
// the attack it must stop is one trivial call to an expensive model to earn a verified
// badge, with every real move played by something cheap. The token is minted per turn by
// the platform, so an agent cannot produce one for a turn it was not handed.
func TestOnlyAProvenCallIsCredited(t *testing.T) {
	const secret, agent, match, round = "s3cret", "ag_1", "m_1", 4
	up := jsonUpstream(t, `{"model":"gpt-5.2","usage":{"prompt_tokens":100,"completion_tokens":20}}`)

	t.Run("valid proof is bound", func(t *testing.T) {
		g, rec := gwFor(t, up, secret)
		tok := turnproof.New(secret).Mint(agent, match, round)
		if w := post(g, agent, match, "4", tok, `{"model":"gpt-5.2"}`); w.Code != 200 {
			t.Fatalf("status %d", w.Code)
		}
		calls, bound := rec.settle(t, 1)
		if !calls[0].Bound {
			t.Fatal("a call with a valid proof was not marked bound")
		}
		if len(bound) != 1 {
			t.Fatalf("BindDecision called %d times, want 1", len(bound))
		}
	})

	t.Run("no proof is forwarded but never credited", func(t *testing.T) {
		g, rec := gwFor(t, up, secret)
		if w := post(g, agent, match, "4", "", `{"model":"gpt-5.2"}`); w.Code != 200 {
			t.Fatalf("a call without a proof was refused (status %d) — the gateway must "+
				"never be the reason a match fails", w.Code)
		}
		calls, bound := rec.settle(t, 1)
		if calls[0].Bound {
			t.Fatal("a call with NO proof was marked bound")
		}
		if len(bound) != 0 {
			t.Fatal("an unproven call earned a decision binding — one cheap call would buy " +
				"a verified badge for a whole match")
		}
	})

	t.Run("a forged proof is never credited", func(t *testing.T) {
		g, rec := gwFor(t, up, secret)
		post(g, agent, match, "4", "obviously-not-a-real-token", `{"model":"gpt-5.2"}`)
		calls, bound := rec.settle(t, 1)
		if calls[0].Bound || len(bound) != 0 {
			t.Fatal("a forged token was accepted")
		}
	})
}

// A token kept from an earlier turn must not make a later one look LLM-backed. Without
// this, one paid call in round 1 would cover a whole match.
func TestAReplayedTokenDoesNotCoverALaterTurn(t *testing.T) {
	const secret, agent, match = "s3cret", "ag_1", "m_1"
	up := jsonUpstream(t, `{"model":"gpt-5.2","usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	g, rec := gwFor(t, up, secret)

	round1Token := turnproof.New(secret).Mint(agent, match, 1)
	// Replay it while claiming round 9.
	post(g, agent, match, "9", round1Token, `{"model":"gpt-5.2"}`)
	calls, bound := rec.settle(t, 1)
	if calls[0].Bound || len(bound) != 0 {
		t.Fatal("a round-1 token verified for round 9 — one paid call would cover an entire match")
	}
}

// Claiming a different match must fail, which is what makes it SAFE for the agent to send
// match and round as plain headers: the token is an HMAC over them.
func TestClaimingAnotherMatchFails(t *testing.T) {
	const secret, agent = "s3cret", "ag_1"
	up := jsonUpstream(t, `{"model":"gpt-5.2","usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	g, rec := gwFor(t, up, secret)

	tok := turnproof.New(secret).Mint(agent, "m_mine", 3)
	post(g, agent, "m_someone_elses", "3", tok, `{"model":"gpt-5.2"}`)
	calls, _ := rec.settle(t, 1)
	if calls[0].Bound {
		t.Fatal("a token minted for one match verified against another")
	}
}

// A proof minted for a DIFFERENT agent must not work, or two colluding accounts could
// share one paid call.
func TestAnotherAgentsProofFails(t *testing.T) {
	const secret = "s3cret"
	up := jsonUpstream(t, `{"model":"gpt-5.2","usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	g, rec := gwFor(t, up, secret)

	tok := turnproof.New(secret).Mint("ag_other", "m_1", 2)
	post(g, "ag_me", "m_1", "2", tok, `{"model":"gpt-5.2"}`)
	calls, _ := rec.settle(t, 1)
	if calls[0].Bound {
		t.Fatal("one agent's token verified for another — colluding accounts could share a call")
	}
}

// ── Rule 1: never be why a match fails ──────────────────────────────────────

// An upstream failure must surface as a failure, not as a gateway crash, and must still be
// recorded — a provider outage is exactly when we most want the data.
func TestUpstreamFailureIsRecordedAndForwarded(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	dead.Close() // nothing is listening

	rec := &fakeRecorder{}
	g := New(Config{Upstreams: map[string]string{"openai": dead.URL}}, rec,
		turnproof.New("s"), slog.New(slog.DiscardHandler))

	w := post(g, "ag_1", "m_1", "1", "", `{"model":"gpt-5.2"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", w.Code)
	}
	if calls, _ := rec.settle(t, 1); calls[0].Status != http.StatusBadGateway {
		t.Fatalf("recorded status %d, want 502", calls[0].Status)
	}
}

// A recorder that fails must not affect the caller at all.
type brokenRecorder struct{}

func (brokenRecorder) RecordCall(context.Context, Call) error {
	return context.DeadlineExceeded
}
func (brokenRecorder) BindDecision(context.Context, string, string, int, string, string, string) error {
	return context.DeadlineExceeded
}
func (brokenRecorder) RecordVerifiedCost(context.Context, VerifiedCost) error {
	return context.DeadlineExceeded
}

func TestBookkeepingFailureNeverBreaksTheCall(t *testing.T) {
	up := jsonUpstream(t, `{"model":"gpt-5.2","usage":{"prompt_tokens":5,"completion_tokens":5}}`)
	g := New(Config{Upstreams: map[string]string{"openai": up.URL}}, brokenRecorder{},
		turnproof.New("s"), slog.New(slog.DiscardHandler))
	if w := post(g, "ag_1", "m_1", "1", "", `{"model":"gpt-5.2"}`); w.Code != 200 {
		t.Fatalf("a database problem became an agent-visible error (status %d)", w.Code)
	}
}

// An unknown provider is refused rather than proxied. Letting an agent name its own
// upstream would make this an SSRF pivot and an open relay.
func TestUnknownProviderIsRefused(t *testing.T) {
	up := jsonUpstream(t, `{}`)
	g, _ := gwFor(t, up, "s")
	r := httptest.NewRequest(http.MethodPost, "/v1/gw/evil/v1/x", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	g.Proxy(w, r, "ag_1", "evil", "/v1/x")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 for an unallowlisted provider", w.Code)
	}
}

// ── Credentials and header hygiene ──────────────────────────────────────────

// The developer's provider key must reach the upstream, and Pyyol's own headers must NOT
// — forwarding them would leak the platform's turn tokens to a third party.
func TestCredentialPassesThroughAndPyyolHeadersDoNot(t *testing.T) {
	up := jsonUpstream(t, `{"model":"gpt-5.2","usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	g, _ := gwFor(t, up, "s3cret")
	tok := turnproof.New("s3cret").Mint("ag_1", "m_1", 1)
	w := post(g, "ag_1", "m_1", "1", tok, `{"model":"gpt-5.2"}`)

	if got := w.Header().Get("X-Upstream-Saw-Auth"); got != "Bearer sk-developers-own-key" {
		t.Fatalf("upstream saw Authorization %q — the developer's own key must pass through, "+
			"since their own bill is what makes the model claim verifiable", got)
	}
	if got := w.Header().Get("X-Upstream-Saw-Pyyol"); got != "" {
		t.Fatalf("the upstream received %s=%q — Pyyol's turn token must never leave the "+
			"platform", HeaderProof, got)
	}
}

// ── Usage extraction ────────────────────────────────────────────────────────

// Anthropic cache WRITES are billed at 1.25x and were never captured anywhere. Missing
// them understates the cost of every caching agent, by an amount that varies with how they
// cache — which silently corrupts cost-efficiency comparison between models.
func TestAnthropicCacheWritesAreCaptured(t *testing.T) {
	up := jsonUpstream(t, `{"model":"claude-opus-4","usage":{
		"input_tokens":500,"output_tokens":120,
		"cache_read_input_tokens":2000,"cache_creation_input_tokens":800}}`)
	g, rec := gwFor(t, up, "s")
	post(g, "ag_1", "m_1", "1", "", `{"model":"claude-opus-4"}`)

	c, _ := rec.settle(t, 1)
	got := c[0]
	if got.CachedWriteTokens != 800 {
		t.Fatalf("cache writes = %d, want 800 — the field the SDK never captured", got.CachedWriteTokens)
	}
	if got.CachedReadTokens != 2000 {
		t.Fatalf("cache reads = %d, want 2000", got.CachedReadTokens)
	}
	// Anthropic reports both cache counts ALONGSIDE input_tokens, so billable input is
	// 500 uncached + 2000 read + 800 written = 3300. Storing the raw 500 would leave a row
	// whose reads alone exceed its prompt total, and pricing — which treats the cache
	// counts as subsets — would clamp the excess away as if it had never been billed.
	if got.PromptTokens != 3300 || got.CompletionTokens != 120 {
		t.Fatalf("tokens %d/%d, want 3300/120", got.PromptTokens, got.CompletionTokens)
	}
	// The invariant the whole normalization exists to hold.
	if got.CachedReadTokens+got.CachedWriteTokens > got.PromptTokens {
		t.Fatalf("cache tokens (%d+%d) exceed prompt total %d — pricing would silently "+
			"clamp the excess to zero", got.CachedReadTokens, got.CachedWriteTokens, got.PromptTokens)
	}
}

func TestOpenAIAndGoogleUsageAreCaptured(t *testing.T) {
	t.Run("openai reasoning + cache", func(t *testing.T) {
		up := jsonUpstream(t, `{"model":"o5","usage":{"prompt_tokens":10,"completion_tokens":90,
			"prompt_tokens_details":{"cached_tokens":4},
			"completion_tokens_details":{"reasoning_tokens":70}}}`)
		g, rec := gwFor(t, up, "s")
		post(g, "ag_1", "m_1", "1", "", `{"model":"o5"}`)
		c, _ := rec.settle(t, 1)
		if c[0].ReasoningTokens != 70 || c[0].CachedReadTokens != 4 {
			t.Fatalf("reasoning=%d cached=%d, want 70/4", c[0].ReasoningTokens, c[0].CachedReadTokens)
		}
	})
	t.Run("google thoughts + cache", func(t *testing.T) {
		up := jsonUpstream(t, `{"usageMetadata":{"promptTokenCount":30,"candidatesTokenCount":15,
			"cachedContentTokenCount":8,"thoughtsTokenCount":40}}`)
		g, rec := gwFor(t, up, "s")
		post(g, "ag_1", "m_1", "1", "", `{"model":"gemini-3"}`)
		c, _ := rec.settle(t, 1)
		if c[0].ReasoningTokens != 40 || c[0].CachedReadTokens != 8 || c[0].PromptTokens != 30 {
			t.Fatalf("got %+v", c[0])
		}
	})
}

// The response's model string wins over the request's, so an alias resolves to the
// concrete version actually served — which is what a model leaderboard must compare.
func TestResolvedModelBeatsTheRequestedAlias(t *testing.T) {
	up := jsonUpstream(t, `{"model":"claude-opus-4-20260501","usage":{"input_tokens":1,"output_tokens":1}}`)
	g, rec := gwFor(t, up, "s")
	post(g, "ag_1", "m_1", "1", "", `{"model":"claude-opus-4-latest"}`)
	c, _ := rec.settle(t, 1)
	if c[0].Model != "claude-opus-4-20260501" {
		t.Fatalf("model = %q, want the resolved version, not the alias", c[0].Model)
	}
}

// ── Streaming ───────────────────────────────────────────────────────────────

// A streamed response must NOT be buffered. Buffering an SSE stream to read its usage
// would delay every token and inflate the latency this platform scores.
func TestStreamedResponsesArePassedThroughUnbuffered(t *testing.T) {
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: {\"delta\":\"a\"}\n\n"))
		w.(http.Flusher).Flush()
		<-release // hold the stream open
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(up.Close)

	rec := &fakeRecorder{}
	g := New(Config{Upstreams: map[string]string{"openai": up.URL}}, rec,
		turnproof.New("s"), slog.New(slog.DiscardHandler))

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- post(g, "ag_1", "m_1", "1", "", `{"model":"gpt-5.2","stream":true}`) }()

	time.Sleep(120 * time.Millisecond)
	close(release)
	w := <-done

	if !strings.Contains(w.Body.String(), `"delta":"a"`) {
		t.Fatal("the streamed chunk never reached the client")
	}
	c, _ := rec.settle(t, 1)
	if !c[0].Streamed {
		t.Fatal("the call was not recorded as streamed")
	}
}

// --- Lens correlation -----------------------------------------------------------
//
// The gateway is the only observer of a provider round trip, so if it does not emit a span the
// developer's trace waterfall has a hole exactly where the slow part was. These pin the fields
// that make the span USEFUL rather than merely present — both of which have silently failed
// before, and neither of which any type check can protect.

type captureEmitter struct {
	on     bool
	events []telemetry.Event
}

func (c *captureEmitter) Enabled() bool               { return c.on }
func (c *captureEmitter) EmitEvent(e telemetry.Event) { c.events = append(c.events, e) }

func TestGatewayEmitsACorrelatedLensSpan(t *testing.T) {
	up := jsonUpstream(t, `{"model":"claude-opus-4","usage":{
		"input_tokens":420,"output_tokens":90,
		"cache_read_input_tokens":1500,"cache_creation_input_tokens":600}}`)
	g, rec := gwFor(t, up, "s")
	em := &captureEmitter{on: true}
	g.SetEmitter(em)

	tok := turnproof.New("s").Mint("ag_1", "m_goofspiel_abc", 7)
	post(g, "ag_1", "m_goofspiel_abc", "7", tok, `{"model":"claude-opus-4"}`)
	if _, _ = rec.settle(t, 1); len(em.events) != 1 {
		t.Fatalf("emitted %d spans, want 1", len(em.events))
	}
	e := em.events[0]

	// Correlation: the trace id must match the one the engine uses for the same match, or the
	// gateway's span renders in a waterfall of its own and the developer never sees it beside
	// their handler.
	if want := telemetry.MatchTraceID("m_goofspiel_abc"); e.TraceID != want {
		t.Errorf("trace = %q, want %q", e.TraceID, want)
	}
	// SessionID is the ARENA, and it is the leaderboard's (agent, game) join key. Leaving it
	// empty made verified gateway cost contribute $0 to every board — the events existed and
	// joined to nothing, which looks exactly like no spend.
	if e.SessionID == "" {
		t.Error("session_id (the arena) is empty — verified cost would join to nothing and read as $0")
	}
	// MeterSource is the STRUCTURAL verified signal. The backend filters verified economics on
	// this column alone, so without it a server-observed call is indistinguishable from a
	// self-reported one however trustworthy it actually was.
	if e.MeterSource != telemetry.MeterSourceGateway {
		t.Errorf("meter_source = %q, want %q", e.MeterSource, telemetry.MeterSourceGateway)
	}
	if e.EventType != benchmark.EventModelCallCompleted {
		t.Errorf("event_type = %q, want the canonical %q", e.EventType, benchmark.EventModelCallCompleted)
	}
	if e.ActorID != "ag_1" || e.RunID != "m_goofspiel_abc" {
		t.Errorf("actor/run = %q/%q", e.ActorID, e.RunID)
	}
	// Cost must be priced on the NORMALIZED tokens, including the cache write that used to be
	// invisible. 420 uncached + 1500 read + 600 written = 2520 billable input.
	if e.PromptTokens != 2520 {
		t.Errorf("prompt tokens = %d, want 2520 (normalized)", e.PromptTokens)
	}
	if e.EstimatedCost <= 0 {
		t.Errorf("estimated cost = %v, want > 0", e.EstimatedCost)
	}
	// turn_bound has to survive into the trace: the ranked integrity check counts bound calls
	// only, so a developer needs to see which of their calls actually counted.
	if b, ok := e.PayloadJSON["turn_bound"].(bool); !ok || !b {
		t.Errorf("turn_bound = %v, want true for a proven call", e.PayloadJSON["turn_bound"])
	}
	if w, ok := e.PayloadJSON["cache_write_tokens"].(int); !ok || w != 600 {
		t.Errorf("cache_write_tokens = %v, want 600", e.PayloadJSON["cache_write_tokens"])
	}
}

func TestGatewayMarksAnUnprovenCallUnboundInTheSpan(t *testing.T) {
	// A forwarded-but-uncredited call must be visibly uncredited. If the span claimed
	// turn_bound=true the trace would disagree with the ledger, and the trace is what a
	// developer trusts when they ask why their coverage is low.
	up := jsonUpstream(t, `{"model":"claude-opus-4","usage":{"input_tokens":10,"output_tokens":5}}`)
	g, rec := gwFor(t, up, "s")
	em := &captureEmitter{on: true}
	g.SetEmitter(em)

	post(g, "ag_1", "m_1", "3", "not-a-real-proof", `{"model":"claude-opus-4"}`)
	if _, _ = rec.settle(t, 1); len(em.events) != 1 {
		t.Fatalf("emitted %d spans, want 1", len(em.events))
	}
	if b, _ := em.events[0].PayloadJSON["turn_bound"].(bool); b {
		t.Error("a forged proof was reported as bound in the trace")
	}
}

func TestGatewayWithoutAnEmitterStillRecords(t *testing.T) {
	// Lens is optional. A deployment with telemetry off must still feed the boards, and a nil
	// or disabled emitter must not panic on the response path.
	up := jsonUpstream(t, `{"model":"m","usage":{"input_tokens":1,"output_tokens":1}}`)
	g, rec := gwFor(t, up, "s")
	g.SetEmitter(&captureEmitter{on: false}) // present but disabled
	post(g, "ag_1", "m_1", "1", "", `{"model":"m"}`)
	if c, _ := rec.settle(t, 1); len(c) != 1 {
		t.Fatalf("recorded %d calls with telemetry disabled, want 1", len(c))
	}
}

// Verified spend must be recorded for EVERY observed call inside a match, bound or not.
//
// This is where VerifiedCostUSD on every board comes from, and the new gateway did not write it
// at all: coverage gating made the verified TIER reachable while the verified COST numerator
// stayed zero, so even a 100%-covered agent fell back to a self-reported cost basis. The bug
// was invisible because both halves looked correct on their own.
func TestVerifiedCostIsRecordedForEveryCallInAMatch(t *testing.T) {
	up := jsonUpstream(t, `{"model":"claude-opus-4","usage":{
		"input_tokens":420,"output_tokens":90,
		"cache_read_input_tokens":1500,"cache_creation_input_tokens":600}}`)
	g, rec := gwFor(t, up, "s")

	// One proven call and one with no proof at all.
	tok := turnproof.New("s").Mint("ag_1", "m_1", 1)
	post(g, "ag_1", "m_1", "1", tok, `{"model":"claude-opus-4"}`)
	post(g, "ag_1", "m_1", "2", "", `{"model":"claude-opus-4"}`)
	rec.settle(t, 2)

	costs := rec.verifiedCosts()
	if len(costs) != 2 {
		t.Fatalf("recorded %d verified-cost entries, want 2 — an UNBOUND call is still a real "+
			"call the developer paid for, and omitting it understates spend in the direction "+
			"that flatters the agent", len(costs))
	}
	for _, c := range costs {
		if c.CostUSD <= 0 {
			t.Errorf("cost %v, want > 0", c.CostUSD)
		}
		// Priced on NORMALIZED tokens: 420 uncached + 1500 read + 600 written.
		if c.PromptTokens != 2520 {
			t.Errorf("prompt tokens = %d, want 2520", c.PromptTokens)
		}
		if c.Model == "" || c.MatchID != "m_1" || c.AgentPublicID != "ag_1" {
			t.Errorf("attribution wrong: %+v", c)
		}
	}
}

func TestNoVerifiedCostOutsideAMatch(t *testing.T) {
	// A call with no match has no match spend to accumulate. Recording one under an empty
	// match id would create a row keyed on "" that every match's totals could collide with.
	up := jsonUpstream(t, `{"model":"m","usage":{"input_tokens":10,"output_tokens":5}}`)
	g, rec := gwFor(t, up, "s")
	post(g, "ag_1", "", "0", "", `{"model":"m"}`)
	rec.settle(t, 1)
	if n := len(rec.verifiedCosts()); n != 0 {
		t.Fatalf("recorded %d verified-cost entries for a matchless call, want 0", n)
	}
}

func TestOneCallIsPricedOnceAndReportedIdentically(t *testing.T) {
	// The recorder and the Lens span must never disagree about what a call cost. They used to
	// price independently, which is two chances to drift on the same number.
	up := jsonUpstream(t, `{"model":"claude-opus-4","usage":{
		"input_tokens":420,"output_tokens":90,"cache_creation_input_tokens":600}}`)
	g, rec := gwFor(t, up, "s")
	em := &captureEmitter{on: true}
	g.SetEmitter(em)
	post(g, "ag_1", "m_1", "1", turnproof.New("s").Mint("ag_1", "m_1", 1), `{"model":"claude-opus-4"}`)
	calls, _ := rec.settle(t, 1)

	if len(em.events) != 1 || len(rec.verifiedCosts()) != 1 {
		t.Fatalf("want one span and one cost entry, got %d/%d", len(em.events), len(rec.verifiedCosts()))
	}
	if calls[0].CostUSD != em.events[0].EstimatedCost {
		t.Errorf("recorder cost %v != span cost %v", calls[0].CostUSD, em.events[0].EstimatedCost)
	}
	if rec.verifiedCosts()[0].CostUSD != calls[0].CostUSD {
		t.Errorf("verified-cost entry %v != call cost %v", rec.verifiedCosts()[0].CostUSD, calls[0].CostUSD)
	}
}

// --- The "Verified" badge -------------------------------------------------------

type fakeAwarder struct {
	mu     sync.Mutex
	agents []string
	fail   bool
}

func (a *fakeAwarder) AwardVerified(_ context.Context, agent string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail {
		return context.DeadlineExceeded
	}
	a.agents = append(a.agents, agent)
	return nil
}
func (a *fakeAwarder) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.agents...)
}

func TestBadgeRequiresAProvenDecisionNotJustARoutedCall(t *testing.T) {
	// The retired gateway awarded this on ANY observed call, so the badge meant "routed a
	// request through us" — the decoration the coverage work exists to replace. A proof binds a
	// call to one (agent, match, round), so a bound call is the smallest thing that shows the
	// pipeline actually worked.
	up := jsonUpstream(t, `{"model":"m","usage":{"input_tokens":10,"output_tokens":5}}`)
	g, rec := gwFor(t, up, "s")
	aw := &fakeAwarder{}
	g.SetAwarder(aw)

	// Unproven: forwarded (rule 1), but earns nothing.
	post(g, "ag_1", "m_1", "1", "forged-proof", `{"model":"m"}`)
	rec.settle(t, 1)
	if n := len(aw.seen()); n != 0 {
		t.Fatalf("badge awarded %d times on an unproven call, want 0", n)
	}

	// Proven: earns it.
	post(g, "ag_1", "m_1", "2", turnproof.New("s").Mint("ag_1", "m_1", 2), `{"model":"m"}`)
	rec.settle(t, 2)
	if got := aw.seen(); len(got) != 1 || got[0] != "ag_1" {
		t.Fatalf("badge awards = %v, want exactly [ag_1]", got)
	}
}

func TestBadgeIsAwardedOncePerAgent(t *testing.T) {
	up := jsonUpstream(t, `{"model":"m","usage":{"input_tokens":10,"output_tokens":5}}`)
	g, rec := gwFor(t, up, "s")
	aw := &fakeAwarder{}
	g.SetAwarder(aw)
	for round := 1; round <= 4; round++ {
		post(g, "ag_1", "m_1", itoa(round), turnproof.New("s").Mint("ag_1", "m_1", round), `{"model":"m"}`)
	}
	rec.settle(t, 4)
	if got := aw.seen(); len(got) != 1 {
		t.Fatalf("badge awarded %d times across 4 proven calls, want 1", len(got))
	}
}

func TestAFailedAwardIsRetriedOnTheNextCall(t *testing.T) {
	// The dedup entry must be released on failure. Keeping it would let one transient database
	// error permanently deny a badge the agent earned, with nothing ever looking again.
	up := jsonUpstream(t, `{"model":"m","usage":{"input_tokens":10,"output_tokens":5}}`)
	g, rec := gwFor(t, up, "s")
	aw := &fakeAwarder{fail: true}
	g.SetAwarder(aw)

	post(g, "ag_1", "m_1", "1", turnproof.New("s").Mint("ag_1", "m_1", 1), `{"model":"m"}`)
	rec.settle(t, 1)
	if n := len(aw.seen()); n != 0 {
		t.Fatalf("award recorded %d despite failing, want 0", n)
	}

	aw.mu.Lock()
	aw.fail = false
	aw.mu.Unlock()
	post(g, "ag_1", "m_1", "2", turnproof.New("s").Mint("ag_1", "m_1", 2), `{"model":"m"}`)
	rec.settle(t, 2)
	if got := aw.seen(); len(got) != 1 {
		t.Fatalf("badge awards after recovery = %v, want one — a failed award must be retryable", got)
	}
}

func TestAFailingBindDoesNotAwardTheBadge(t *testing.T) {
	// The badge stands for a RECORDED proven decision. Awarding it when the binding failed would
	// leave a developer holding a badge the boards cannot corroborate.
	up := jsonUpstream(t, `{"model":"m","usage":{"input_tokens":10,"output_tokens":5}}`)
	aw := &fakeAwarder{}
	g := New(Config{Upstreams: map[string]string{"openai": up.URL, "anthropic": up.URL}},
		brokenRecorder{}, turnproof.New("s"), slog.New(slog.DiscardHandler))
	g.SetAwarder(aw)
	rr := post(g, "ag_1", "m_1", "1", turnproof.New("s").Mint("ag_1", "m_1", 1), `{"model":"m"}`)
	if rr.Code != 200 {
		t.Fatalf("code = %d, want 200 — bookkeeping failure must never break the call", rr.Code)
	}
	time.Sleep(150 * time.Millisecond)
	if n := len(aw.seen()); n != 0 {
		t.Fatalf("badge awarded %d times despite a failed binding, want 0", n)
	}
}
