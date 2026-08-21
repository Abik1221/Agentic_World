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
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agent-arena/arena/internal/benchmark"
	"github.com/agent-arena/arena/internal/movebind"
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
	//
	// move/completionHash/receipt carry the COMPLETION BINDING and are empty when the
	// response held no recognisable move tool call. An implementation must treat empty as
	// "no move bound" and must not overwrite a move already recorded for this turn with
	// nothing — an agent that makes a bound move call and then a plain follow-up call would
	// otherwise erase its own binding, which is both a correctness bug and a trivial way to
	// opt out of the check.
	BindDecision(ctx context.Context, matchID, agentPublicID string, round int, move, completionHash, receipt string) error
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

// KindReader resolves an agent's kind — `external` for a developer's agent, `harness` for
// one of the platform's own benchmark seats.
//
// Read-only and OPTIONAL. Without it, spans carry no kind and Lens shows the traffic
// undifferentiated, which is the behaviour before this existed. It must never be able to
// fail a call: this feeds observability, and refusing a developer's model call because a
// telemetry label could not be looked up would be a strictly worse platform.
type KindReader interface {
	AgentKind(ctx context.Context, agentPublicID string) (string, error)
}

// Verifier checks a turn proof and mints the completion-binding receipt. Satisfied by
// *turnproof.Signer.
//
// One interface rather than two because both operations are the same secret used in the
// same request: a deployment that can verify a turn can always mint the receipt for it, and
// splitting them would allow a half-configured gateway that binds calls it cannot attest.
type Verifier interface {
	Verify(agentPublicID, matchID string, round int, token string) bool
	MintDecision(agentPublicID, matchID string, round int, completionHash, move string) string
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
	// UpstreamHost is the host the gateway actually dialled, resolved from the configured
	// upstream map rather than from anything the agent sent.
	//
	// It is what separates a measurement from a recording. `Provider` and `Model` say what
	// the developer ASKED for and is billed for; this says who answered. In a lab the two
	// routinely disagree — anthropic pointed at a local stand-in returns a perfectly
	// well-formed, bindable response under the model name that was requested — and without
	// this field nothing downstream can tell that call apart from a real one.
	UpstreamHost string
	// CostUSD is what this call cost, priced from the NORMALIZED token counts by the versioned
	// table. Computed once here and carried, so the recorder and the Lens span cannot report
	// two different costs for one call — and so pricing runs once rather than per consumer.
	CostUSD float64

	// --- Completion binding (internal/movebind, internal/turnproof) ---
	//
	// CompletionHash pins the exact response bytes the gateway observed. ExtractedMove is the
	// canonical move read out of that response's structured tool call. Receipt is the HMAC over
	// (agent, match, round, hash, move) that lets a replay re-verify the binding without
	// trusting the stored row.
	//
	// All three are empty whenever the response carried no recognisable move tool call. That is
	// the normal case for an agent that has not adopted the structured-move contract, and it
	// means UNBOUND, never "wrong move" — see the enforcement rule in the game services.
	CompletionHash string
	ExtractedMove  string
	Receipt        string

	// BoundRounds is every round this ONE completion decided.
	//
	// Usually exactly one — the round the turn proof attests. An agent that batches ("plan
	// rounds 4 through 6 in a single call") produces several, all sharing CompletionHash and
	// each carrying its own receipt over its own move.
	//
	// This exists because coverage used to count CALLS, which made the honest floor for a
	// batching agent ~33% while Phase 4 rewards batching as cost optimisation. ExtractedMove
	// and Receipt above stay the PROVEN round's, so the trace and the span keep describing the
	// decision the request was made for.
	BoundRounds []BoundRound

	// UsageUnreadable marks a successful response whose usage we could not parse. Carried on
	// the Call so the condition is visible in the trace and countable, not just a log line —
	// the whole risk is that it stays invisible while the call is priced at zero.
	UsageUnreadable bool
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
		// Already used in practice via an override. Shipping it as a default means its
		// host is publishable without an operator having to declare it by hand.
		"openrouter": "https://openrouter.ai/api",
	}
}

// PublishableUpstreamHosts returns the hosts whose responses may be published as MODEL
// measurements — on the harness benchmark, the model board, anywhere a model is ranked.
//
// The problem it solves is specific. provider/model are read from the request, and the
// upstream map decides where that request actually goes. Point anthropic at a local
// stand-in and the gateway records a bound, well-formed `anthropic / claude-opus-4` call
// that never left the machine. Every lab run does exactly this, and afterwards nothing on
// the row distinguishes it from a real call.
//
// The default set is the vendor endpoints this binary ships, which gives the two behaviours
// that matter without anyone configuring anything:
//
//   - a production deployment on the defaults publishes everything, as before;
//   - a lab pointing a provider at a stand-in publishes nothing from it, because the
//     stand-in's host is not in the set.
//
// A deployment with a legitimate override — an enterprise egress proxy, a self-hosted vLLM
// whose results it genuinely wants ranked — declares that host explicitly. Requiring the
// declaration is the point: publishing a model ranking is a claim about a model, and the
// operator should have to say which hosts they stand behind.
func PublishableUpstreamHosts(extra string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, base := range DefaultUpstreams() {
		if h := upstreamHost(base); h != "" {
			out[h] = struct{}{}
		}
	}
	for _, raw := range strings.Split(extra, ",") {
		// Accept a bare host or a full URL, because an operator copying from
		// LLM_GATEWAY_UPSTREAMS will paste whichever they have to hand.
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if h := upstreamHost(v); h != "" {
			out[h] = struct{}{}
			continue
		}
		out[v] = struct{}{}
	}
	return out
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
// upstreamHost reduces a configured upstream base URL to its host, for recording on each
// call. Ports are kept: a stand-in and a real provider can share a hostname and differ
// only by port, and collapsing them would erase exactly the distinction being recorded.
//
// An unparseable base returns "" — UNKNOWN, never a guess. Everything downstream treats
// an empty host as "provenance not established" and refuses to publish it as a model
// measurement, so a malformed config costs us a row on the board rather than putting an
// unverifiable one on it.
func upstreamHost(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

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
	// kinds resolves an agent's kind for the Lens span. Nil leaves spans unlabelled.
	kinds KindReader
	// kindCache memoises agentPublicID → kind for the life of the process.
	//
	// Safe to cache without expiry, and that is a property of the schema rather than an
	// assumption: an agent's kind is written at INSERT and there is no update path for it
	// anywhere in the codebase, deliberately, because relabelling an agent would not move
	// the matches it already played. A value that cannot change cannot go stale.
	kindCache sync.Map
}

// SetKindReader wires agent-kind labelling of Lens spans. Optional; nil leaves spans
// unlabelled rather than guessing a kind.
func (g *Gateway) SetKindReader(k KindReader) { g.kinds = k }

// agentKind resolves the kind for a span, memoised, and answers "" for anything it cannot
// determine.
//
// Empty is deliberately NOT defaulted to `external`. An unknown-kind span is an honest gap;
// a span mislabelled `external` puts platform benchmark traffic back into a developer's
// telemetry, which is the exact confusion this label exists to remove.
func (g *Gateway) agentKind(ctx context.Context, agentPublicID string) string {
	if g.kinds == nil || agentPublicID == "" {
		return ""
	}
	if v, ok := g.kindCache.Load(agentPublicID); ok {
		return v.(string)
	}
	kind, err := g.kinds.AgentKind(ctx, agentPublicID)
	if err != nil {
		// Not cached: a transient read error must not pin this agent to "unknown" for the
		// rest of the process's life. Not logged at anything above debug either — this is a
		// label on a trace, and a noisy warning per call would be worse than the gap.
		g.log.Debug("llmgw: could not resolve agent kind for the Lens span",
			"agent", agentPublicID, "error", err)
		return ""
	}
	g.kindCache.Store(agentPublicID, kind)
	return kind
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
		// WHERE this call actually went, captured here because this is the only moment the
		// fact exists. Provider and Model are read from the request — what the developer
		// asked for — and `base` is what LLM_GATEWAY_UPSTREAMS resolved that to. Point a
		// provider at a local stand-in and the two disagree completely, with nothing on the
		// row to say so afterwards.
		UpstreamHost: upstreamHost(base),
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
	// The upstream wait is over and the first byte is about to go out; the deadline the router
	// armed at request arrival may already have passed.
	armWrite(w)
	w.WriteHeader(resp.StatusCode)

	// Stream through, TEEING both shapes into a bounded buffer.
	//
	// Streamed responses used to be copied straight through and never captured, on the
	// reasoning that reading an SSE stream would delay every token. That conflated two
	// different things. BUFFERING — holding bytes back until the body ends — would indeed
	// add latency; TEEING does not, because the client's copy is written and flushed first
	// and the buffer only receives what has already gone out. The cost of the conflation was
	// large and silent: a streamed call recorded zero tokens and zero cost, so every
	// streaming agent contributed nothing to verified spend while looking measured. It also
	// made completion binding impossible for exactly the agents most likely to stream.
	//
	// Bounded because a response is attacker-influenced in size: an agent that asked for a
	// million tokens must not be able to make the gateway hold them all in memory. Past the
	// cap the tee stops recording and the client's stream continues untouched — losing the
	// binding for that call, never the call itself.
	captured := &capBuffer{limit: maxCapturedBytes}
	_, copyErr := copyFlushing(io.MultiWriter(w, captured), resp.Body, w)
	call.LatencyMS = time.Since(start).Milliseconds()

	if body := captured.Bytes(); len(body) > 0 && !captured.truncated {
		// ONE normalizer for every provider and both transports. See usagenorm.go: fields are
		// matched by what they MEAN, so a provider nobody has listed is costed correctly.
		if !applyUsageGeneric(&call, body) && call.Status >= 200 && call.Status <= 299 {
			// The one condition worth alerting on. A successful call whose usage we could not
			// read is a real provider being costed at ZERO — silently, because a zero is a
			// plausible integer. On a platform that ranks cost efficiency that is not merely a
			// gap, it is a way to win, and nothing in any test would ever notice.
			call.UsageUnreadable = true
			g.log.Warn("llmgw: could not read usage from a successful response — this call is "+
				"costed at ZERO and its provider shape is unknown to us",
				"agent", call.AgentPublicID, "provider", call.Provider, "model", call.Model,
				"streamed", call.Streamed, "bytes", len(body),
				"fix", "add a case to internal/llmgw/usagenorm.go classifyUsageKey, and a "+
					"fixture to sdk/conformance/usage_pricing.json")
		}
		g.bindCompletion(&call, body)
	}
	// Price once, on the normalized counts, before anything consumes the call.
	// Priced BY PROVIDER, not by model name alone. "llama-3.3-70b" is the same string whether
	// you run it yourself (free) or Groq serves it (billed), and pricing it by name recorded
	// $0 for every Groq-backed agent — on a cost-efficiency board, being unmeasurable is a way
	// to win. The gateway always knows who served the call, so it is the one caller that can
	// never get this wrong.
	call.CostUSD = pricing.EstimateCostFor(call.Provider, call.Model, call.PromptTokens,
		call.CompletionTokens, call.CachedReadTokens, call.CachedWriteTokens, call.ReasoningTokens)

	// AND SAY SO WHEN THE RATE WAS A GUESS.
	//
	// A model with no entry in the price table is charged a plausible mid-range rate rather
	// than zero — deliberately, because zero is a way to win a cost-efficiency board. But an
	// estimate that nothing announces is only better than zero by degree: it still ends up
	// printed as "$0.004 per win" beside figures derived from real published rates, and
	// nothing on the row says which is which.
	//
	// Once per model, not per call: a benchmark makes thousands of calls per model and a line
	// each would bury exactly the signal this is for. WARN rather than Info because the window
	// to act is before the results are published, and the operator is reading this log during
	// the run — see pricing.UnpricedModels for the full list at any point.
	if pricing.PriceBasis(call.Model) == "estimated" && pricing.NoteUnpricedModel(call.Model) {
		g.log.Warn("llmgw: no price table entry for this model — its cost is an ESTIMATE and "+
			"any cost-efficiency ranking including it is an estimate too",
			"provider", call.Provider, "model", call.Model,
			"fix", "add the model's published rates to internal/pricing before publishing a benchmark")
	}

	if copyErr != nil {
		// WARN, not Debug. A body that ends early is indistinguishable from a short answer to
		// everything downstream: nothing binds, usage reads as unknown, and the seat is recorded
		// as not having played. Debug level meant the one event that explains all three was the
		// one event nobody could see — the same failure mode UsageUnreadable exists to prevent.
		g.log.Warn("llmgw: the upstream response body ended early — this call binds nothing and "+
			"is costed at zero, and the agent will be recorded as not having played",
			"agent", agentPublicID, "provider", call.Provider, "model", call.Model,
			"captured_bytes", captured.buf.Len(), "error", copyErr)
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
		// Every round this completion decided gets its own row. For an agent that does not
		// batch that is exactly one — the proven round — and identical to what this always did.
		//
		// A completion carrying NO recognisable move still binds its proven round with an empty
		// move, because "a verified call was made for this turn" is true and is what the ranked
		// gate counts. That is the case BoundRounds is empty for.
		bound := c.BoundRounds
		if len(bound) == 0 {
			bound = []BoundRound{{Round: c.Round}}
		}
		persisted := 0
		for _, br := range bound {
			if err := g.rec.BindDecision(ctx, c.MatchID, c.AgentPublicID, br.Round,
				br.Move, c.CompletionHash, br.Receipt); err != nil {
				// Logged per round rather than abandoning the span: rounds are independent
				// rows, and losing round 5 to a transient error is no reason to drop round 6.
				g.log.Debug("llmgw: could not bind decision",
					"match", c.MatchID, "round", br.Round, "error", err)
				continue
			}
			persisted++
		}
		// The badge stands for a RECORDED proven decision, so it needs at least one row to
		// have landed. Awarding it off an attempted binding would leave a developer holding a
		// badge the boards cannot corroborate.
		if persisted == 0 {
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
	Usage usageUsage `json:"usage"`
	// Google Gemini
	UsageMetadata struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	Model string `json:"model"`
}

// usageUsage is the `usage` object itself, named so the streaming path can accumulate one
// across frames. Anonymous, it could only ever be decoded whole — which is precisely what a
// provider that splits its counts over several frames never sends.
type usageUsage struct {
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
}

// applyUsage fills in what the provider reported. Server-observed, so unlike the SDK's
// numbers it is not a claim the agent can shade.
func applyUsage(c *Call, body []byte) {
	var s usageShape
	if json.Unmarshal(body, &s) != nil {
		return
	}
	applyUsageShape(c, s)
}

// applyUsageShape normalizes one decoded usage object onto the Call.
//
// Split from applyUsage so the streamed path, which must assemble its shape from many
// frames before it has one to normalize, runs the SAME conversion rather than a second copy
// of it.
func applyUsageShape(c *Call, s usageShape) {
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

// maxCapturedBytes bounds the tee. Large enough for any real completion including a long
// reasoning trace, small enough that a hostile response cannot pressure the gateway's memory
// — with many concurrent calls the product of the two is what matters, not one response.
const maxCapturedBytes = 4 << 20 // 4 MiB

// # Why the proxy re-arms the connection's write deadline
//
// The router gives every ordinary request a FIXED write deadline, set once when the request
// arrives, because the server itself runs WriteTimeout=0 so that SSE can work. For a normal
// handler that is right. For this one it is a bug, and a subtle one: a proxied model call spends
// almost all of its life waiting on the upstream, and only then starts writing. A reasoning model
// that thinks for 30 seconds therefore reaches its first write with the deadline ALREADY expired,
// the write fails instantly after a few hundred bytes, and the client receives a truncated body.
//
// The damage was not a clean error. Downstream it surfaced as three unrelated-looking faults —
// nothing bound, usage unreadable so the call costed zero, and the agent recorded as not having
// played, which the arena covers with a fallback move. The effect scaled with how long a model
// thinks, so it fell hardest on exactly the reasoning models a benchmark most wants to measure,
// and it looked like those models were failing to answer.
//
// The long-lived paths already solved this with a ROLLING deadline re-armed before each write,
// and that is the correct shape here too: it bounds any single stalled write, so a black-hole
// client still cannot wedge a goroutine, while placing no ceiling at all on how long the upstream
// may think. A larger fixed deadline would have been the wrong fix — it only moves the cliff.
const proxyWriteGrace = 60 * time.Second

// armWrite (re)arms the rolling per-write deadline. Best-effort by design: a ResponseWriter that
// cannot expose a deadline is left unbounded rather than failing the call, matching rule 1 — the
// gateway must never be the reason a match fails.
func armWrite(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(proxyWriteGrace))
}

// capBuffer accumulates up to limit bytes and then stops, remembering that it did.
//
// The truncated flag is the point. A silently short buffer would be parsed as if complete:
// usage would read as zero and a half-received tool call could decode to a DIFFERENT move
// than the model emitted — which at match time would reject an honest turn. So a truncated
// capture is discarded entirely rather than trusted in part.
type capBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	// Always reports a full write: this is a tee, and returning a short write would abort
	// io.MultiWriter and with it the CLIENT's copy. Losing our bookkeeping is acceptable;
	// truncating the agent's response is rule 1.
	return len(p), nil
}

func (c *capBuffer) Bytes() []byte { return c.buf.Bytes() }

// bindCompletion extracts the move the model produced and mints the receipt for it.
//
// # Why only on a BOUND call
//
// The extraction is meaningful only if we know which decision it belongs to. On an unbound
// call the match and round are unverified headers the agent chose, so recording an extracted
// move against them would let an agent write a move of its choosing into any turn's slot —
// turning the anti-cheat control into the cheat. So this runs after the turn proof verified,
// never before.
//
// # Why a failure here is silent
//
// Every exit is "leave it unbound". A completion with no tool call, an unknown game, a shape
// we could not parse — all of them mean the platform has nothing to say about this move, and
// the match must proceed exactly as it does today. The only thing that may ever REJECT a turn
// is a successful extraction that DISAGREES with the submitted move.
// BoundRound is one round a completion decided, with the receipt attesting it.
//
// A separate receipt per round rather than one over the whole span: each row in
// agent_match_bound_decisions is read and verified on its own at match time, and a receipt
// covering a list would force a reader checking round 5 to reconstruct rounds 4 and 6 to
// verify it. The COMPLETION HASH is what ties them back together as one call.
type BoundRound struct {
	Round   int
	Move    string
	Receipt string
}

func (g *Gateway) bindCompletion(c *Call, body []byte) {
	if !c.Bound || c.Status < 200 || c.Status > 299 || g.verifier == nil {
		return
	}
	game := telemetry.GameFromMatchID(c.MatchID)
	tool := movebind.ToolFor(game)
	if tool == "" {
		return
	}
	var tc movebind.ToolCall
	var ok bool
	if c.Streamed {
		tc, ok = movebind.ExtractStream(body, tool)
	} else {
		tc, ok = movebind.Extract(body, tool)
	}
	if !ok {
		return
	}
	// Every round this completion decided. A plain call yields exactly one — the proven
	// round — so this is the same behaviour it has always had for an agent that does not batch.
	covered, ok := movebind.CanonPlan(game, tc, c.Round)
	if !ok {
		return
	}
	hash := movebind.CompletionHash(body)
	c.CompletionHash = hash
	c.BoundRounds = make([]BoundRound, 0, len(covered))
	for _, rm := range covered {
		receipt := g.verifier.MintDecision(c.AgentPublicID, c.MatchID, rm.Round, hash, rm.Move)
		c.BoundRounds = append(c.BoundRounds, BoundRound{Round: rm.Round, Move: rm.Move, Receipt: receipt})
		// The PROVEN round's move is what the trace and the Lens span report, because that is
		// the decision this request was made for. A span's later rounds are recorded, not
		// narrated.
		if rm.Round == c.Round {
			c.ExtractedMove, c.Receipt = rm.Move, receipt
		}
	}
	if len(c.BoundRounds) > 1 {
		g.log.Debug("llmgw: one completion covered several rounds",
			"agent", c.AgentPublicID, "match", c.MatchID, "proven_round", c.Round,
			"rounds_covered", len(c.BoundRounds))
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
		// ACCEPT-ENCODING IS DROPPED, and this one is load-bearing.
		//
		// Go's transport adds its own Accept-Encoding and transparently decompresses the
		// response — but ONLY when it added the header itself. Forwarding the agent's header
		// disables that, so the gateway received COMPRESSED bytes and teed them into the
		// capture buffer. Both readers then failed on gzip:
		//
		//   usage   → "could not read usage" on all 26 calls of a live Groq match, costed $0
		//   binding → 24 bound rows with ZERO extracted moves, against 26/26 for a provider
		//             that does not compress
		//
		// The usage half was LOUD, as designed. The binding half failed SILENTLY, because
		// "no move extracted" is indistinguishable from "the agent sent no move tool call" —
		// and that must never reject, which is exactly the rule that hid it. So completion
		// binding, the strongest control on the platform, was inert for every provider that
		// gzips. Groq does, by default.
		//
		// Dropping the header costs one uncompressed hop between the gateway and the agent and
		// buys a body every reader can actually read.
		if hopByHop[lk] || strings.HasPrefix(lk, "x-pyyol-") || lk == "host" ||
			lk == "content-length" || lk == "accept-encoding" {
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
			// Rolling: each write gets the full grace, so a slow-but-progressing stream is
			// never reaped while a stalled one still is.
			armWrite(w)
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
	// Detached and short, for the same reason record() detaches: by the time emit runs the
	// client's context is already finished, so inheriting it would cancel the lookup on
	// every single call. Bounded tightly because this sits on the response path — a slow
	// database must cost the span its label, not the request its latency.
	kindCtx, cancelKind := context.WithTimeout(context.Background(), 2*time.Second)
	agentKind := g.agentKind(kindCtx, c.AgentPublicID)
	cancelKind()

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
		// Whose traffic this is. The platform's harness plays real matches through this same
		// gateway, so without this an operator tracing a benchmark run is reading developer
		// telemetry and a developer's cost view contains calls that were never theirs.
		AgentKind: agentKind,
		LatencyMS: int64(c.LatencyMS),
		Priority:  telemetry.PriorityHigh,
		PayloadJSON: map[string]any{
			"turn": c.Round,
			// Whether this call is provably the one made for that turn. The ranked integrity
			// check counts bound calls only, so the distinction has to survive into the trace.
			"turn_bound": c.Bound,
			// Cache WRITE tokens, which nothing captured before this session and which bill at
			// 1.25x input. Carried so a developer can see where a cache-heavy turn's cost went.
			"cache_write_tokens": c.CachedWriteTokens,
			// The move the MODEL produced, as the gateway read it. Empty means no structured
			// move tool call was found. In the trace this is the line a developer compares
			// against the move their agent actually submitted when a turn is rejected — without
			// it, "move did not match the model's output" is an assertion they cannot check.
			"extracted_move": c.ExtractedMove,
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
