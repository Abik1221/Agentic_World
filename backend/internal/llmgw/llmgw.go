// Package llmgw is the Pyyol LLM Gateway: a pass-through proxy that turns "which model
// did this agent really use" from a claim into an observation.
//
// # Why a proxy rather than attestation
//
// Every self-reported alternative fails the same way. A manifest field, a signed
// telemetry blob, an SDK that swears it called Claude — all of them are statements by the
// party with the incentive to lie, and a competitive leaderboard with cash prizes is
// exactly where that incentive bites. The measured audit was unambiguous: 0 of 2,579
// recorded decisions carried verified attribution, and the only model labels on the
// platform were strings agents asserted about themselves.
//
// A proxy is different because the developer BRINGS THEIR OWN KEY. They cannot claim
// claude-opus without Anthropic billing them for claude-opus. Verification becomes
// incentive-compatible rather than trust-based: the proof is their own invoice.
//
// # What makes it anti-cheat rather than merely observant
//
// Routing through a gateway on its own proves only that a call happened SOMEWHERE. The
// obvious attack is to send one trivial call to an expensive model, earn the verified
// badge, and play every real move from something cheap. So every call carries a per-turn
// token the platform minted (internal/turnproof), and a call is CREDITED only when that
// token verifies against the exact (agent, match, round) it claims. An agent cannot mint
// a token for a turn it was not handed, and a token kept from round 1 verifies as round 1.
//
// The honest limit, stated rather than papered over: this proves a real model call was
// made FOR this decision. It cannot prove the model's output was the move that was
// played — that is unknowable from outside the agent, and inferring it from move content
// would be both evadable and unfair to legitimate agents.
//
// # Three rules that decide every design choice here
//
//  1. THE GATEWAY MUST NEVER BE WHY A MATCH FAILS. A proxy on the critical path of every
//     decision is a new outage surface. So it fails OPEN on everything except credit: an
//     unverifiable proof, a recording error, a broken database — the call is still
//     forwarded and the agent still plays. Only the CREDIT is withheld.
//  2. IT MUST ADD ESSENTIALLY NO LATENCY. Latency is a scored signal on this platform, so
//     a slow gateway would corrupt the very measurement it exists to enable. Responses
//     stream straight through with flushing; nothing is buffered; bookkeeping happens
//     after the bytes are delivered, never in front of them.
//  3. IT MUST NEVER STORE A DEVELOPER'S PROVIDER KEY. The key is read from the request and
//     handed to the upstream. It is not logged, not persisted, and not held after the
//     request. Anything else makes Pyyol a credential custodian and one leak catastrophic.
package llmgw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agent-arena/arena/internal/benchmark"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/pricing"
)

// Recorder persists what the gateway observed. Implemented by store.
//
// Called off the response path, so an implementation may be slow but must not panic.
type Recorder interface {
	// RecordCall stores one observed model call. A call with Bound=false is still
	// recorded — an unbound call is evidence too, and the gap between total and bound
	// calls is exactly the coverage figure a verified leaderboard must publish.
	RecordCall(ctx context.Context, c Call) error
	// BindDecision marks (match, agent, round) as proven LLM-backed. Only ever called
	// for a call whose turn proof verified.
	BindDecision(ctx context.Context, matchID, agentPublicID string, round int) error
	// RecordVerifiedCost accumulates per-match server-observed spend.
	//
	// Separate from RecordCall because it answers a different question and has a different
	// grain: RecordCall is the per-call audit trail, this is the per-match total the boards
	// divide. Called on EVERY observed call, bound or not — an unbound call is still a real
	// call the developer really paid for, and a cost total that omitted them would understate
	// spend in exactly the direction that flatters the agent.
	RecordVerifiedCost(ctx context.Context, c VerifiedCost) error
}

// VerifiedCost is one observed call's contribution to a match's verified spend.
type VerifiedCost struct {
	MatchID          string
	AgentPublicID    string
	CostUSD          float64
	Provider         string
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Awarder grants the developer-visible "Verified" badge, once, the first time an agent proves
// a decision was LLM-backed.
//
// On the FIRST BOUND CALL, not the first observed one. The older gateway awarded it on any
// observed call, which made the badge mean "routed at least one request through us" — the same
// decoration the coverage work exists to replace. A proof binds a call to a specific
// (agent, match, round), so a bound call is the smallest thing that actually demonstrates the
// pipeline works end to end, and it is the least a badge should stand for.
//
// Optional: a deployment without it simply does not award badges.
type Awarder interface {
	AwardVerified(ctx context.Context, agentPublicID string) error
}

// Emitter ships a Lens span for an observed call. Satisfied by *telemetry.Client.
//
// Separate from Recorder because the two answer different questions and fail
// independently: the recorder feeds the boards and the ledger, the emitter feeds the trace
// waterfall a developer opens when a turn went wrong. Losing one must not cost the other.
//
// Optional, like the recorder — a deployment with Lens disabled still records calls.
type Emitter interface {
	EmitEvent(telemetry.Event)
	Enabled() bool
}

// Verifier checks a turn proof. Satisfied by *turnproof.Signer.
type Verifier interface {
	Verify(agentPublicID, matchID string, round int, token string) bool
	Enabled() bool
}

// Call is one observed model call.
type Call struct {
	AgentPublicID string
	// MatchID / Round are what the agent CLAIMED. Meaningless unless Bound is true —
	// the proof is what makes them trustworthy, not the headers.
	MatchID string
	Round   int
	// Bound reports whether the turn proof verified for exactly this (agent, match,
	// round). This single field is the difference between a measurement and a claim.
	Bound bool
	// Provider / Model are read from the REQUEST, which is what the developer is billed
	// for. That is the whole basis of the guarantee: they cannot name a model they are
	// not paying for.
	Provider string
	Model    string
	// Usage as reported by the provider's own response. Absent on a streamed response
	// unless the caller opted into usage chunks — see extractUsage.
	PromptTokens     int
	CompletionTokens int
	CachedReadTokens int
	// CachedWriteTokens is billed ABOVE the normal input rate (Anthropic charges 1.25x
	// for a cache write and 0.1x for a read), so folding it into prompt tokens
	// understates cost by an amount that varies with how the agent uses caching — which
	// silently corrupts any cost-efficiency comparison between models.
	CachedWriteTokens int
	ReasoningTokens   int
	// LatencyMS is the upstream call alone, measured by the platform. Distinct from the
	// turn latency the arena already measures, which also contains the developer's own
	// prompt building and parsing. Attributing that to a model would charge a developer's
	// slow code to their provider.
	LatencyMS int64
	Status    int
	Streamed  bool
	// CostUSD is what this call cost, priced from the NORMALIZED token counts by the versioned
	// table. Computed once here and carried, so the recorder and the Lens span cannot report
	// two different costs for one call — and so pricing runs once rather than per consumer.
	CostUSD float64
}

// Config tunes the gateway.
type Config struct {
	// Upstreams maps a provider slug to its base URL. Absent providers are refused, so
	// the gateway can never be turned into an open relay to arbitrary hosts.
	Upstreams map[string]string
	// Timeout bounds one upstream call. Generous: a reasoning model legitimately takes
	// minutes, and cutting it off here would make the gateway the reason a slow-but-honest
	// agent loses a round.
	Timeout time.Duration
}

// DefaultUpstreams are the providers the gateway will proxy to.
//
// An allowlist, not a passthrough. Letting an agent name its own upstream host would make
// this an SSRF pivot into the platform's network and an open relay for anyone who found
// the URL.
func DefaultUpstreams() map[string]string {
	return map[string]string{
		"openai":    "https://api.openai.com",
		"anthropic": "https://api.anthropic.com",
		"google":    "https://generativelanguage.googleapis.com",
		"groq":      "https://api.groq.com",
		"mistral":   "https://api.mistral.ai",
		"deepseek":  "https://api.deepseek.com",
	}
}

// UpstreamsFromEnv parses a "slug=url,slug=url" override list onto the defaults.
//
// Exists for three real reasons, not for tests: a self-hosted deployment may front its own
// vLLM or Ollama; an enterprise may require provider traffic to leave through their own
// egress proxy; and a provider changing a hostname must not need a Pyyol release. Entries
// merge onto the defaults, so overriding one provider does not silently remove the rest.
//
// Still an allowlist afterwards — this widens what an OPERATOR permits, never what an agent
// can request. An agent naming its own upstream would be an SSRF pivot and an open relay.
func UpstreamsFromEnv(raw string) map[string]string {
	out := DefaultUpstreams()
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		slug, url, ok := strings.Cut(pair, "=")
		slug, url = strings.ToLower(strings.TrimSpace(slug)), strings.TrimSpace(url)
		if !ok || slug == "" || url == "" {
			continue // a malformed entry is ignored, never turned into a wildcard
		}
		out[slug] = url
	}
	return out
}

// Gateway proxies model calls and records what it saw.
type Gateway struct {
	cfg      Config
	rec      Recorder
	verifier Verifier
	client   *http.Client
	log      *slog.Logger
	// coverage answers "how much of this agent's play was verified". Optional: without it
	// the endpoint reports unavailable rather than inventing a figure.
	coverage CoverageReader
	// em ships Lens spans for observed calls. Nil (or disabled) simply skips them.
	em Emitter
	// award grants the "Verified" badge on an agent's first proven decision. Nil disables it.
	award Awarder
	// awarded dedups the badge in-process. The award itself is idempotent, so this is a
	// courtesy to the database rather than a correctness requirement.
	awarded sync.Map
}

// SetCoverageReader wires verified-coverage reporting. Nil leaves it unavailable.
// SetEmitter attaches the Lens emitter. Optional: without it calls are still recorded, they
// just do not appear in the trace waterfall.
func (g *Gateway) SetEmitter(em Emitter) { g.em = em }

// SetAwarder attaches the "Verified" badge granter. Optional.
func (g *Gateway) SetAwarder(a Awarder) { g.award = a }

func (g *Gateway) SetCoverageReader(c CoverageReader) {
	if c != nil {
		g.coverage = c
	}
}

func New(cfg Config, rec Recorder, v Verifier, log *slog.Logger) *Gateway {
	if cfg.Upstreams == nil {
		cfg.Upstreams = DefaultUpstreams()
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	return &Gateway{
		cfg: cfg, rec: rec, verifier: v, log: log,
		client: &http.Client{
			Timeout: cfg.Timeout,
			// No redirect following: a provider that redirects could otherwise send the
			// developer's API key to a host we never allowlisted.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("llmgw: upstream redirects are not followed")
			},
		},
	}
}

// Headers the SDK sets. Named once so the two SDKs and the gateway cannot drift.
const (
	HeaderProof    = "X-Pyyol-Proof" // the per-turn token the platform minted
	HeaderMatch    = "X-Pyyol-Match" // the match the agent claims this call is for
	HeaderTurn     = "X-Pyyol-Turn"  // the round it claims
	HeaderProvider = "X-Pyyol-Provider"
	// HeaderKey carries the caller's PYYOL agent key.
	//
	// It cannot be the standard Authorization header, and that is not a stylistic choice: a
	// gateway request already uses the provider's own credential slot. Anthropic reads
	// x-api-key, OpenAI reads "Authorization: Bearer", and both must arrive upstream
	// untouched. Putting Pyyol's identity there would either overwrite a developer's OpenAI
	// key or be mistaken for one.
	//
	// Stripped before forwarding by the x-pyyol- rule in copyUpstreamHeaders, so a Pyyol
	// credential is never handed to a model provider.
	HeaderKey = "X-Pyyol-Key"
)

// Proxy handles one model call: verify, forward, stream back, then record.
//
// agentPublicID comes from the authenticated principal, NEVER from a header — it is the
// one identity in this flow the agent does not get to assert.
func (g *Gateway) Proxy(w http.ResponseWriter, r *http.Request, agentPublicID, providerSlug, upstreamPath string) {
	base, ok := g.cfg.Upstreams[providerSlug]
	if !ok {
		http.Error(w, `{"error":{"message":"unknown provider"}}`, http.StatusBadRequest)
		return
	}

	// Read the request body: it is small, and we need the model name out of it. The
	// RESPONSE is the thing that must never be buffered.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		http.Error(w, `{"error":{"message":"request body too large or unreadable"}}`, http.StatusBadRequest)
		return
	}

	call := Call{
		AgentPublicID: agentPublicID,
		MatchID:       r.Header.Get(HeaderMatch),
		Provider:      providerSlug,
	}
	call.Round, _ = strconv.Atoi(r.Header.Get(HeaderTurn))
	call.Model, call.Streamed = peekRequest(body)

	// THE anti-cheat step. The agent supplies match and round in headers, and that is
	// safe precisely because the token is an HMAC over (agent, match, round): claiming a
	// different match or round makes the token fail to verify. So the headers are not
	// trusted — the proof is what makes them trustworthy.
	if g.verifier != nil && call.MatchID != "" {
		call.Bound = g.verifier.Verify(agentPublicID, call.MatchID, call.Round,
			r.Header.Get(HeaderProof))
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method,
		strings.TrimRight(base, "/")+"/"+strings.TrimLeft(upstreamPath, "/"), bytes.NewReader(body))
	if err != nil {
		http.Error(w, `{"error":{"message":"could not build upstream request"}}`, http.StatusBadGateway)
		return
	}
	copyUpstreamHeaders(r.Header, req.Header)

	start := time.Now()
	resp, err := g.client.Do(req)
	if err != nil {
		// Rule 1: never be the reason a match fails. Surface the upstream failure as
		// itself so the agent's own error handling behaves as it would without us.
		call.LatencyMS = time.Since(start).Milliseconds()
		call.Status = http.StatusBadGateway
		g.record(call)
		http.Error(w, `{"error":{"message":"upstream request failed"}}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	call.Status = resp.StatusCode
	for k, vs := range resp.Header {
		if hopByHop[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Stream through. A tee is used only for non-streamed responses, where the body is a
	// single JSON object we need usage from. A streamed response is copied straight to the
	// client with flushing so we add no perceptible latency — buffering an SSE stream to
	// read its usage would delay every token and corrupt the latency we are measuring.
	var captured bytes.Buffer
	dst := io.Writer(w)
	if !call.Streamed {
		dst = io.MultiWriter(w, &captured)
	}
	_, copyErr := copyFlushing(dst, resp.Body, w)
	call.LatencyMS = time.Since(start).Milliseconds()

	if !call.Streamed && captured.Len() > 0 {
		applyUsage(&call, captured.Bytes())
	}
	// Price once, on the normalized counts, before anything consumes the call.
	call.CostUSD = pricing.EstimateCost(call.Model, call.PromptTokens, call.CompletionTokens,
		call.CachedReadTokens, call.CachedWriteTokens, call.ReasoningTokens)
	if copyErr != nil {
		g.log.Debug("llmgw: response copy ended early", "agent", agentPublicID, "error", copyErr)
	}
	g.record(call)
}

// record persists a call off the response path.
//
// Detached context and a fresh timeout: the client's context is already finished by the
// time we get here, and inheriting it would cancel every write. Errors are logged at
// debug and never surfaced — rule 1 again, a bookkeeping failure must not become an
// agent-visible error.
func (g *Gateway) record(c Call) {
	g.emit(c)
	if g.rec == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := g.rec.RecordCall(ctx, c); err != nil {
			g.log.Debug("llmgw: could not record call", "agent", c.AgentPublicID, "error", err)
		}
		// Verified spend, on every observed call within a match. This is where VerifiedCostUSD
		// on every board comes from; without it the verified cost BASIS is unreachable no
		// matter how complete an agent's coverage is, because the numerator stays zero.
		if c.MatchID != "" {
			if err := g.rec.RecordVerifiedCost(ctx, VerifiedCost{
				MatchID: c.MatchID, AgentPublicID: c.AgentPublicID, CostUSD: c.CostUSD,
				Provider: c.Provider, Model: c.Model,
				PromptTokens: c.PromptTokens, CompletionTokens: c.CompletionTokens,
				TotalTokens: c.PromptTokens + c.CompletionTokens,
			}); err != nil {
				g.log.Debug("llmgw: could not record verified cost", "match", c.MatchID, "error", err)
			}
		}
		// Only a verified call earns the binding. This is the single line that separates
		// a verified leaderboard from a decorative badge.
		if !c.Bound || c.Status < 200 || c.Status > 299 {
			return
		}
		if err := g.rec.BindDecision(ctx, c.MatchID, c.AgentPublicID, c.Round); err != nil {
			g.log.Debug("llmgw: could not bind decision", "match", c.MatchID, "error", err)
			return
		}
		g.awardVerified(ctx, c.AgentPublicID)
	}()
}

// peekRequest pulls the model name and streaming flag out of a request body.
//
// Deliberately tolerant: an unrecognised body shape yields an empty model rather than an
// error, because refusing a call we merely failed to parse would break agents using a
// provider shape we have not met yet.
func peekRequest(body []byte) (model string, streamed bool) {
	var probe struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &probe)
	return probe.Model, probe.Stream
}

// usageShape covers the response fields the three major providers use. One struct rather
// than three extractors: the field sets are disjoint, so a single decode cannot confuse
// one provider's numbers for another's.
type usageShape struct {
	Usage struct {
		// OpenAI chat completions
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		// Anthropic messages
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	// Google Gemini
	UsageMetadata struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	Model string `json:"model"`
}

// applyUsage fills in what the provider reported. Server-observed, so unlike the SDK's
// numbers it is not a claim the agent can shade.
func applyUsage(c *Call, body []byte) {
	var s usageShape
	if json.Unmarshal(body, &s) != nil {
		return
	}
	u := s.Usage
	switch {
	case u.PromptTokens > 0 || u.CompletionTokens > 0: // OpenAI
		c.PromptTokens, c.CompletionTokens = u.PromptTokens, u.CompletionTokens
		c.CachedReadTokens = u.PromptTokensDetails.CachedTokens
		c.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	case u.InputTokens > 0 || u.OutputTokens > 0 ||
		u.CacheReadInputTokens > 0 || u.CacheCreationInputTokens > 0: // Anthropic
		c.PromptTokens, c.CompletionTokens = u.InputTokens, u.OutputTokens
		c.CachedReadTokens = u.CacheReadInputTokens
		// The field the SDK never captured. Billed at 1.25x, so omitting it understates
		// the cost of every caching agent.
		c.CachedWriteTokens = u.CacheCreationInputTokens
		// Anthropic reports both cache counts ALONGSIDE input_tokens, not inside it, so
		// input_tokens alone is only the uncached remainder. Every consumer downstream —
		// pricing above all — treats the cache counts as SUBSETS of prompt tokens, which
		// is true of OpenAI and false here. Storing the raw value would leave a row whose
		// reads exceed its prompt total, and pricing would clamp the excess away as if it
		// had never been billed. Normalizing here keeps one convention across both the
		// observed-call table and the SDK-reported path.
		c.PromptTokens += u.CacheReadInputTokens + u.CacheCreationInputTokens
	case s.UsageMetadata.PromptTokenCount > 0: // Google
		c.PromptTokens = s.UsageMetadata.PromptTokenCount
		c.CompletionTokens = s.UsageMetadata.CandidatesTokenCount
		c.CachedReadTokens = s.UsageMetadata.CachedContentTokenCount
		c.ReasoningTokens = s.UsageMetadata.ThoughtsTokenCount
	}
	// The response's own model string beats the request's when present: it resolves an
	// alias like "claude-3-5-sonnet-latest" to the concrete version actually served,
	// which is what a model leaderboard has to compare.
	if s.Model != "" {
		c.Model = s.Model
	}
}

// hopByHop headers must not be forwarded in either direction.
var hopByHop = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-authenticate": true,
	"proxy-authorization": true, "te": true, "trailer": true,
	"transfer-encoding": true, "upgrade": true,
}

// copyUpstreamHeaders forwards what the provider needs and nothing else.
//
// The developer's provider credential passes THROUGH — read from this request, handed to
// the upstream, never logged and never persisted. Pyyol holding provider keys would make
// one breach catastrophic, and the whole trust proposition of a verified tier collapses if
// developers reasonably fear that.
//
// Pyyol's own headers are stripped: they are instructions to us, not to the provider, and
// forwarding them would leak the platform's turn tokens to a third party.
func copyUpstreamHeaders(src, dst http.Header) {
	for k, vs := range src {
		lk := strings.ToLower(k)
		if hopByHop[lk] || strings.HasPrefix(lk, "x-pyyol-") || lk == "host" ||
			lk == "content-length" {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// copyFlushing streams src to dst, flushing after every chunk when the writer supports it.
//
// Without the flush an SSE stream is buffered and the agent sees nothing until the model
// finishes — which would turn a streaming provider into a non-streaming one and inflate
// the latency this platform scores.
func copyFlushing(dst io.Writer, src io.Reader, w http.ResponseWriter) (int64, error) {
	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	var total int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			written, werr := dst.Write(buf[:n])
			total += int64(written)
			if canFlush {
				flusher.Flush()
			}
			if werr != nil {
				return total, werr
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return total, nil
			}
			return total, rerr
		}
	}
}

// emit ships one server-observed model call to Lens, correlated to the match trace.
//
// This is what makes a gateway call visible in the same waterfall as the agent's own spans:
// the trace id is match_<match_id> on both sides, so a developer debugging a slow turn sees
// the provider round trip next to their handler rather than having to infer it.
//
// Two fields here are load-bearing and were learned the hard way, so they are set explicitly
// rather than left to a default:
//
//   - SessionID is the ARENA. It is the leaderboard's (agent, game) join key, and without it
//     verified gateway cost silently contributed $0 to every board — the events existed and
//     joined to nothing.
//   - MeterSource=gateway is the STRUCTURAL verified signal. The backend filters verified
//     economics on that column alone, so an event without it is indistinguishable from a
//     self-reported one however trustworthy its provenance actually was.
//
// Synchronous, unlike the recorder: the emitter is already a non-blocking queue that drops
// under pressure, so wrapping it in another goroutine would add a scheduling hop and a second
// place for the same event to be lost.
func (g *Gateway) emit(c Call) {
	if g.em == nil || !g.em.Enabled() {
		return
	}
	status := "ok"
	if c.Status < 200 || c.Status > 299 {
		status = "error"
	}
	total := c.PromptTokens + c.CompletionTokens
	g.em.EmitEvent(telemetry.Event{
		TraceID: telemetry.MatchTraceID(c.MatchID),
		// The canonical constant from benchmark, not a local copy. llmgateway mirrored this
		// string in its own package; a third copy would be one more place for the cost
		// analytics query's event filter to silently stop matching.
		EventType:        benchmark.EventModelCallCompleted,
		Status:           status,
		StepName:         "gateway.model_call",
		SpanType:         "model_call",
		Operation:        "model_call",
		ActorID:          c.AgentPublicID,
		SessionID:        telemetry.GameFromMatchID(c.MatchID),
		RunID:            c.MatchID,
		Provider:         c.Provider,
		Model:            c.Model,
		PromptTokens:     int64(c.PromptTokens),
		CompletionTokens: int64(c.CompletionTokens),
		CachedTokens:     int64(c.CachedReadTokens),
		ReasoningTokens:  int64(c.ReasoningTokens),
		TotalTokens:      int64(total),
		EstimatedCost:    c.CostUSD,
		PricingVersion:   pricing.Version,
		Currency:         telemetry.CurrencyUSD,
		MeterSource:      telemetry.MeterSourceGateway,
		LatencyMS:        int64(c.LatencyMS),
		Priority:         telemetry.PriorityHigh,
		PayloadJSON: map[string]any{
			"turn": c.Round,
			// Whether this call is provably the one made for that turn. The ranked integrity
			// check counts bound calls only, so the distinction has to survive into the trace.
			"turn_bound": c.Bound,
			// Cache WRITE tokens, which nothing captured before this session and which bill at
			// 1.25x input. Carried so a developer can see where a cache-heavy turn's cost went.
			"cache_write_tokens": c.CachedWriteTokens,
		},
	})
}

// awardVerified grants the badge once per agent per process.
//
// On failure the dedup entry is REMOVED so a later call retries. Keeping it would mean one
// transient database error permanently denies a badge the agent earned, and nothing would ever
// look at it again — a silent, unrecoverable loss for the developer.
func (g *Gateway) awardVerified(ctx context.Context, agentPublicID string) {
	if g.award == nil || agentPublicID == "" {
		return
	}
	if _, seen := g.awarded.LoadOrStore(agentPublicID, struct{}{}); seen {
		return
	}
	if err := g.award.AwardVerified(ctx, agentPublicID); err != nil {
		g.awarded.Delete(agentPublicID)
		g.log.Debug("llmgw: could not award verified badge", "agent", agentPublicID, "error", err)
	}
}
