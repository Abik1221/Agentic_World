// Package llmgateway is the Pyyol LLM Gateway: a transparent reverse proxy that
// sits between a developer's agent and their LLM provider (OpenAI, Anthropic).
//
// Why it exists (from the telemetry/anti-cheat architecture): manifest-declared
// model + self-reported tokens cannot be trusted for competitive/ranked leaderboards
// — a dev can claim gpt-4o-mini while running gpt-4o. When the SDK routes ranked
// traffic through this gateway (base_url swap), Pyyol OBSERVES the real request and
// response, so the model, token counts, latency, and cost are server-measured and
// unfakeable. The developer still passes their OWN provider key (forwarded upstream
// untouched); the gateway only observes and attributes, it does not mint tokens.
//
// This is the verified (Tier 2) counterpart to the SDK's sandbox (Tier 1)
// auto-instrumentation. Both emit the same `model_call_completed` event; gateway
// events additionally carry `verified: true`.
//
// Design: transparent (request/response bytes forwarded verbatim), non-blocking
// observation (a parse/emit failure never affects the proxied call), streaming
// responses pass straight through (no usage available on the stream — documented
// limitation, same as the SDK), and every upstream is allow-listed.
package llmgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/pricing"
)

// Emitter is the minimal telemetry sink the gateway needs (satisfied by
// *telemetry.Client; a capturing fake in tests).
type Emitter interface {
	Enabled() bool
	EmitEvent(telemetry.Event)
}

// Authenticator resolves a Pyyol agent key to an agent id. ok=false ⇒ 401. The
// default rejects empty keys and treats a non-empty key as the agent id; production
// injects a store-backed validator (idSvc.ResolveAgentKey) via WithAuthenticator.
type Authenticator func(ctx context.Context, key string) (agentID string, ok bool)

// EventModelCallCompleted mirrors the benchmark package's canonical event so gateway
// and SDK/arena costs land in one cost-analytics surface.
const EventModelCallCompleted = "model_call_completed"

type upstream struct {
	name    string
	baseURL string // no trailing slash
}

// Proxy is the gateway HTTP handler. Mount it under a base path; it routes by the
// first path segment: /openai/... → OpenAI, /anthropic/... → Anthropic.
// VerifiedHook is called (best-effort, off the response's critical path) on every
// observed verified call, with the agent, match (from X-Pyyol-Match; may be ""), and
// the server-measured USD cost. The wiring layer uses it to (a) accumulate per-match
// verified cost and (b) award the "Verified" badge once. Must be fast/non-blocking or
// spawn its own goroutine — it runs inline after the response is written.
type VerifiedHook func(ctx context.Context, agentID, matchID string, costUSD float64)

type Proxy struct {
	em         Emitter
	client     *http.Client
	auth       Authenticator
	log        *slog.Logger
	now        func() time.Time
	upstreams  map[string]upstream
	onVerified VerifiedHook
}

// Option configures a Proxy.
type Option func(*Proxy)

// WithUpstream overrides an upstream base URL (used by tests to point at a fake).
func WithUpstream(name, baseURL string) Option {
	return func(p *Proxy) { p.upstreams[name] = upstream{name: name, baseURL: strings.TrimRight(baseURL, "/")} }
}

// WithAuthenticator injects the agent-key validator.
func WithAuthenticator(a Authenticator) Option { return func(p *Proxy) { p.auth = a } }

// WithHTTPClient overrides the upstream client (timeouts, transport).
func WithHTTPClient(c *http.Client) Option { return func(p *Proxy) { p.client = c } }

// WithClock overrides the clock (tests).
func WithClock(now func() time.Time) Option { return func(p *Proxy) { p.now = now } }

// WithVerifiedHook sets the callback fired when a verified call is observed (used to
// award the "Verified" badge).
func WithVerifiedHook(h VerifiedHook) Option { return func(p *Proxy) { p.onVerified = h } }

func defaultAuth(_ context.Context, key string) (string, bool) {
	if key == "" {
		return "", false
	}
	return key, true
}

// New builds a gateway proxy. em may be nil/disabled (observation becomes a no-op;
// proxying still works).
func New(em Emitter, log *slog.Logger, opts ...Option) *Proxy {
	if log == nil {
		log = slog.Default()
	}
	p := &Proxy{
		em:     em,
		client: &http.Client{Timeout: 120 * time.Second},
		auth:   defaultAuth,
		log:    log,
		now:    time.Now,
		upstreams: map[string]upstream{
			"openai":    {name: "openai", baseURL: "https://api.openai.com"},
			"anthropic": {name: "anthropic", baseURL: "https://api.anthropic.com"},
		},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// hopByHop headers must not be forwarded (RFC 7230 §6.1).
var hopByHop = map[string]bool{
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true,
	"Transfer-Encoding": true, "Te": true, "Trailer": true, "Upgrade": true,
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Identify the Pyyol agent (does NOT consume the provider key — that stays in
	//    Authorization and is forwarded upstream untouched).
	agentID, ok := p.auth(r.Context(), r.Header.Get("X-Pyyol-Key"))
	if !ok {
		http.Error(w, `{"error":"pyyol_unauthorized: missing or invalid X-Pyyol-Key"}`, http.StatusUnauthorized)
		return
	}

	// 2. Route by the first path segment.
	seg, rest := splitFirstSegment(r.URL.Path)
	up, known := p.upstreams[seg]
	if !known {
		http.Error(w, `{"error":"pyyol_unknown_upstream"}`, http.StatusNotFound)
		return
	}

	// 3. Build the upstream request, forwarding method, path, query, body, and all
	//    headers except hop-by-hop and Pyyol control headers.
	target := up.baseURL + rest
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, `{"error":"pyyol_bad_gateway"}`, http.StatusBadGateway)
		return
	}
	for k, vs := range r.Header {
		if hopByHop[http.CanonicalHeaderKey(k)] || strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Pyyol-") {
			continue
		}
		for _, v := range vs {
			outReq.Header.Add(k, v)
		}
	}
	outReq.ContentLength = r.ContentLength

	start := p.now()
	resp, err := p.client.Do(outReq)
	if err != nil {
		http.Error(w, `{"error":"pyyol_upstream_unreachable"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	latencyMS := p.now().Sub(start).Milliseconds()

	// 4. Copy response headers + status back to the caller.
	for k, vs := range resp.Header {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}

	// Streaming responses (SSE) pass straight through — no usage on the stream.
	if isStream(resp) {
		w.WriteHeader(resp.StatusCode)
		flushCopy(w, resp.Body)
		return
	}

	// 5. Buffer the JSON response so we can OBSERVE usage, then forward it verbatim.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		// We already committed to proxying; send what we have.
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)

	// 6. Observe + emit (best-effort; never affects the proxied response). Only on
	//    2xx — errors carry no billable usage.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		p.observe(up.name, agentID, r.Header, latencyMS, body)
	}
}

// observe extracts real usage from the upstream response, prices it, and emits a
// server-verified model_call_completed event attributed to the agent + match.
func (p *Proxy) observe(provider, agentID string, reqHeader http.Header, latencyMS int64, body []byte) {
	u, ok := extractUsage(body)
	if !ok {
		return
	}
	cost := pricing.EstimateCost(u.Model, u.PromptTokens, u.CompletionTokens, u.CachedTokens, u.ReasoningTokens)
	match := reqHeader.Get("X-Pyyol-Match")
	// The agent produced a real, gateway-observed LLM call: accumulate its verified
	// cost (per match) and let the wiring award the "Verified" badge. Fires regardless
	// of whether Lens is enabled.
	if p.onVerified != nil && agentID != "" {
		p.onVerified(context.Background(), agentID, match, cost)
	}
	if p.em == nil || !p.em.Enabled() {
		return
	}
	trace := telemetry.MatchTraceID(match)
	p.em.EmitEvent(telemetry.Event{
		TraceID:   trace,
		EventType: EventModelCallCompleted,
		Status:    "ok",
		StepName:  "gateway.model_call",
		SpanType:  "model_call",
		Operation: "model_call",
		ActorID:   agentID,
		// session_id = the arena. Without it this event never matched the leaderboard's
		// (agent, game) join key, so verified gateway cost silently contributed $0.
		SessionID:        telemetry.GameFromMatchID(match),
		RunID:            match,
		Provider:         provider,
		Model:            u.Model,
		PromptTokens:     int64(u.PromptTokens),
		CompletionTokens: int64(u.CompletionTokens),
		CachedTokens:     int64(u.CachedTokens),
		ReasoningTokens:  int64(u.ReasoningTokens),
		TotalTokens:      int64(u.total()),
		EstimatedCost:    cost,
		PricingVersion:   pricing.Version,
		Currency:         telemetry.CurrencyUSD,
		// meter_source=gateway is the STRUCTURAL "verified" signal: server-observed,
		// unfakeable. The backend filters verified economics on this column alone.
		MeterSource: telemetry.MeterSourceGateway,
		LatencyMS:   latencyMS,
		Priority:    telemetry.PriorityHigh,
		PayloadJSON: map[string]any{
			"turn": reqHeader.Get("X-Pyyol-Turn"),
		},
	})
}

// usage is the normalized token usage parsed from an upstream response.
type usage struct {
	Model            string
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	ReasoningTokens  int
	TotalTokens      int
}

func (u usage) total() int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.PromptTokens + u.CompletionTokens
}

// extractUsage parses an OpenAI- or Anthropic-shaped JSON response body. Returns
// ok=false when there is no usable usage (e.g. an error body).
func extractUsage(body []byte) (usage, bool) {
	var raw struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			InputTokens         int `json:"input_tokens"`  // anthropic
			OutputTokens        int `json:"output_tokens"` // anthropic
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
			CacheReadInputTokens int `json:"cache_read_input_tokens"` // anthropic
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return usage{}, false
	}
	u := usage{Model: raw.Model, TotalTokens: raw.Usage.TotalTokens}
	u.PromptTokens = raw.Usage.PromptTokens
	if u.PromptTokens == 0 {
		u.PromptTokens = raw.Usage.InputTokens
	}
	u.CompletionTokens = raw.Usage.CompletionTokens
	if u.CompletionTokens == 0 {
		u.CompletionTokens = raw.Usage.OutputTokens
	}
	u.CachedTokens = raw.Usage.PromptTokensDetails.CachedTokens
	if u.CachedTokens == 0 {
		u.CachedTokens = raw.Usage.CacheReadInputTokens
	}
	u.ReasoningTokens = raw.Usage.CompletionTokensDetails.ReasoningTokens
	if u.PromptTokens == 0 && u.CompletionTokens == 0 {
		return usage{}, false
	}
	return u, true
}

func splitFirstSegment(path string) (seg, rest string) {
	p := strings.TrimPrefix(path, "/")
	i := strings.IndexByte(p, '/')
	if i < 0 {
		return p, "/"
	}
	return p[:i], p[i:]
}

func isStream(resp *http.Response) bool {
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
}

// flushCopy streams the body to the client, flushing so SSE tokens arrive live.
func flushCopy(w http.ResponseWriter, src io.Reader) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
