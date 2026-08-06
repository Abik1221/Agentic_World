// Package benchmark models the server-authoritative, per-decision quality
// signals of a match — the data a real agent benchmark is built on: how often an
// agent produced a legal move on time, versus how often the engine had to
// substitute a deterministic fallback because the agent was illegal, slow,
// errored, or offline. It is pure (no I/O) so the aggregation math is trivially
// unit-tested; the telemetry emission lives in emit.go.
//
// Why this exists: these substitutions happen inside the drive loops and were
// previously invisible. Fallback/timeout/illegal-move rates are THE difference
// between a strong agent and a broken one, and they can't be faked (the engine
// measures them), so they are the trustworthy core of the benchmark.
package benchmark

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/agent-arena/arena/internal/pricing"
)

// Outcome classifies one decision the engine asked an agent to make.
type Outcome string

const (
	// OutcomeOK: the agent returned a legal move within the time budget.
	OutcomeOK Outcome = "ok"
	// OutcomeIllegal: the agent answered, but with an illegal move → fallback.
	OutcomeIllegal Outcome = "illegal_move"
	// OutcomeTimeout: the agent did not answer within the turn budget → fallback.
	OutcomeTimeout Outcome = "timeout"
	// OutcomeTransportError: the socket send/recv failed → fallback.
	OutcomeTransportError Outcome = "transport_error"
	// OutcomeDisconnected: the agent had no live socket → fallback.
	OutcomeDisconnected Outcome = "disconnected"
	// OutcomeError: the agent returned an explicit error frame → fallback.
	OutcomeError Outcome = "error"
)

// Fallback reports whether this outcome forced the engine to substitute a
// deterministic legal move for the agent.
func (o Outcome) Fallback() bool { return o != OutcomeOK }

// Result is a seat's match outcome, set once at match end (empty = unknown/not
// recorded). It powers the win-rate leaderboard.
type Result string

const (
	ResultWin  Result = "win"
	ResultLoss Result = "loss"
	ResultDraw Result = "draw"
)

// Decision is one recorded ask→answer at a seat.
type Decision struct {
	Seat      int
	AgentID   string
	Outcome   Outcome
	LatencyMS int64
	// Per-move detail for observability (does not affect aggregates):
	Round     int         // engine round/turn number, if known
	Action    string      // the action kind the agent chose (e.g. "vote", "buy", card value)
	Rationale string      // optional agent-supplied reasoning for the move
	Usage     *TokenUsage // optional per-move LLM token usage (drives cost/economics)
	// View is the turn view the agent was handed for this decision — the INPUT half of
	// the record. Serialized and size-capped by the Recorder; nil is fine.
	//
	// Without it a developer's trace shows the answer and not the question: "reasoned X,
	// played 9, illegal" is unactionable, while the same line beside the state the agent
	// was looking at is a bug report. This is the single most useful thing the platform
	// can hand back, and it was the one thing never recorded.
	View any
}

// TokenUsage is the optional per-move LLM economics an agent may report with its
// move. When present it powers token/cost analytics; absent it costs nothing. The
// SDK's auto-instrumentation fills these from the provider's own response, so Model
// here is the REAL per-move model (not just the manifest-declared one).
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
	// Prompt-cache READS and WRITES, kept apart because they are separately billed and
	// point opposite ways (on Anthropic, 0.1x input for a read and 1.25x for a write).
	// Collapsing them into one number makes the cost unrecoverable from what we stored.
	CachedTokens      int `json:"cached_tokens,omitempty"`
	CachedWriteTokens int `json:"cached_write_tokens,omitempty"`
	TotalTokens       int `json:"total_tokens,omitempty"`
	// Model/Provider observed for THIS move (SDK-reported from the actual call).
	// Empty falls back to the seat's manifest-declared model.
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	// Scaffold fingerprints the HARNESS this decision ran under — system prompt, tools,
	// sampling — with the model deliberately excluded. Holding it constant is what turns
	// "Claude beats GPT" from a confounded observation into a paired comparison.
	Scaffold string `json:"scaffold,omitempty"`
	// ScaffoldUnstable means the fingerprint changed mid-turn, which happens when variable
	// game state sits in the system prompt. Such a decision cannot be paired, and that has
	// to travel with the data rather than be guessed at later.
	ScaffoldUnstable bool `json:"scaffold_unstable,omitempty"`
	// ModelCalls is how many model calls this ONE decision took, and CallLatenciesMS how
	// long each took. An aggregate cannot separate one slow call from six quick ones.
	ModelCalls      int   `json:"model_calls,omitempty"`
	CallLatenciesMS []int `json:"call_latencies_ms,omitempty"`
}

// total returns the reported total, or the sum of the parts if total is unset.
func (u TokenUsage) total() int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.PromptTokens + u.CompletionTokens + u.ReasoningTokens
}

// DecisionDetail is one recorded move, kept in the seat's decision log so the
// full per-agent decision trail — including reasoning — reaches observability.
type DecisionDetail struct {
	// At is when the engine ASKED for this move — the anchor a waterfall needs.
	//
	// DERIVED, not measured directly: the Recorder is called immediately after a
	// decision returns, so At = (record time − latency). The ask time is the right
	// anchor rather than the answer time because it is what makes the GAPS between
	// turns visible, and the gaps are the point of a waterfall. An agent that answered
	// in 200ms inside an 8-second phase window looks fine on a latency bar and is
	// obviously not the bottleneck on a timeline.
	At        time.Time   `json:"at"`
	Round     int         `json:"round"`
	Action    string      `json:"action,omitempty"`
	Outcome   string      `json:"outcome"`
	LatencyMS int64       `json:"latency_ms"`
	Rationale string      `json:"rationale,omitempty"`
	Usage     *TokenUsage `json:"usage,omitempty"`
	// Input is the JSON-encoded turn view the agent was given. Omitted when the view was
	// absent, unserializable, or over the per-decision size cap — see recordInput.
	Input json.RawMessage `json:"input,omitempty"`
	// InputTruncated marks a view that was dropped for size. A developer must be able to
	// tell "the agent was given nothing" from "we did not keep what it was given";
	// rendering both as an empty pane would make the first look like an engine bug.
	InputTruncated bool `json:"input_truncated,omitempty"`
}

// maxDecisionLog bounds per-seat decision detail so one match can't emit an
// unbounded payload. Only real agent seats are recorded, so this is generous.
const maxDecisionLog = 256

// Input-capture size caps.
//
// A turn view is the biggest thing in this record by an order of magnitude — a
// mid-game Monopoly view carries the whole board — and there can be 256 of them per
// seat. Two caps, because one is not enough:
//
//   - maxInputBytes bounds a SINGLE view, so one pathological state cannot dominate.
//   - maxSeatInputBytes bounds the seat's TOTAL, so a long match cannot multiply a
//     merely-large view into an event payload the outbox chokes on.
//
// Hitting either cap sets InputTruncated rather than silently storing nothing: a
// developer needs to distinguish "the agent got no view" from "we did not keep it".
const (
	maxInputBytes     = 16 << 10  // 16 KiB per decision
	maxSeatInputBytes = 512 << 10 // 512 KiB per seat, across the whole match
)

// SeatSummary aggregates every decision made at one seat in a match.
type SeatSummary struct {
	Seat            int    `json:"seat"`
	AgentID         string `json:"agent_id"`
	AgentVersion    string `json:"agent_version,omitempty"` // manifest version, for version-diff
	Provider        string `json:"provider,omitempty"`      // declared model provider (manifest)
	Model           string `json:"model,omitempty"`         // declared model (manifest)
	Decisions       int64  `json:"decisions"`
	Legal           int64  `json:"legal"`
	Illegal         int64  `json:"illegal"`
	Timeouts        int64  `json:"timeouts"`
	TransportErrors int64  `json:"transport_errors"`
	Disconnects     int64  `json:"disconnects"`
	Errors          int64  `json:"errors"`
	Fallbacks       int64  `json:"fallbacks"`
	LatencySumMS    int64  `json:"latency_sum_ms"`
	LatencyMinMS    int64  `json:"latency_min_ms"`
	LatencyMaxMS    int64  `json:"latency_max_ms"`
	Result          Result `json:"result,omitempty"` // match outcome for this seat
	// LLM economics (summed across moves that reported usage; 0 if none did).
	PromptTokens      int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens  int64 `json:"completion_tokens,omitempty"`
	ReasoningTokens   int64 `json:"reasoning_tokens,omitempty"`
	CachedTokens      int64 `json:"cached_tokens,omitempty"`
	CachedWriteTokens int64 `json:"cached_write_tokens,omitempty"`
	TotalTokens       int64 `json:"total_tokens,omitempty"`
	// EstimatedCost is the summed USD cost across moves that reported usage, priced
	// per-move by the versioned pricing table (real per-move model when reported,
	// else the manifest model). PricingVersion records which table produced it.
	EstimatedCost  float64 `json:"estimated_cost,omitempty"`
	PricingVersion string  `json:"pricing_version,omitempty"`
	// Full per-move trail (capped) — action, outcome, latency, reasoning, the view the
	// agent was given, and token usage for every decision, so observability can see WHY
	// an agent moved and at what cost, not just aggregate rates.
	DecisionLog []DecisionDetail `json:"decision_log,omitempty"`

	// inputBytes tracks how much captured view this seat has accumulated, against
	// maxSeatInputBytes. Unexported so it never reaches the wire — it is a budget
	// counter, not a fact about the match.
	inputBytes int
}

// LegalRate is the fraction of decisions that were legal + on time (0..1). An
// empty seat reports 0.
func (s SeatSummary) LegalRate() float64 {
	if s.Decisions == 0 {
		return 0
	}
	return float64(s.Legal) / float64(s.Decisions)
}

// FallbackRate is the fraction of decisions the engine had to substitute (0..1).
func (s SeatSummary) FallbackRate() float64 {
	if s.Decisions == 0 {
		return 0
	}
	return float64(s.Fallbacks) / float64(s.Decisions)
}

// AvgLatencyMS is the mean ask→answer latency; 0 for an empty seat.
func (s SeatSummary) AvgLatencyMS() float64 {
	if s.Decisions == 0 {
		return 0
	}
	return float64(s.LatencySumMS) / float64(s.Decisions)
}

// ObservedModel is the model this seat ACTUALLY called, as reported per-move by the
// SDK, or ("","") when no move reported one.
//
// This is a stronger claim than the seat's manifest-declared Provider/Model: the
// manifest says what the developer intends to run and is written once, while this is
// read back from the calls the agent made during this specific match. The model
// benchmark prefers it for exactly that reason.
//
// The MODE (most frequent) move wins rather than the first or last, because an agent
// may legitimately make a cheap warm-up or fallback call on a different model, and a
// single such call must not relabel the whole match. Ties break toward the earlier
// model so the result is deterministic for a given decision log.
func (s SeatSummary) ObservedModel() (provider, model string) {
	type key struct{ provider, model string }
	counts := make(map[key]int)
	var order []key
	for _, d := range s.DecisionLog {
		if d.Usage == nil || d.Usage.Model == "" {
			continue
		}
		k := key{d.Usage.Provider, d.Usage.Model}
		if counts[k] == 0 {
			order = append(order, k)
		}
		counts[k]++
	}
	var best key
	var bestN int
	for _, k := range order { // insertion order ⇒ deterministic tie-break
		if counts[k] > bestN {
			best, bestN = k, counts[k]
		}
	}
	return best.provider, best.model
}

// MatchSummary is the full per-match benchmark fact (one per match, low volume →
// safe to route through the durable path).
type MatchSummary struct {
	Game    string        `json:"game"`
	MatchID string        `json:"match_id"`
	Seats   []SeatSummary `json:"seats"`
}

// Recorder accumulates decisions for one match. Safe for concurrent use (drive
// loops are single-goroutine per match today, but a match may fan out later).
type Recorder struct {
	game    string
	matchID string

	// now is injectable so decision timestamps are assertable. Never nil after
	// NewRecorder.
	now func() time.Time

	mu    sync.Mutex
	seats map[int]*SeatSummary
	order []int // seat insertion order, for a stable Summary
}

// RecorderOption configures a Recorder.
type RecorderOption func(*Recorder)

// WithClock overrides the Recorder's clock (tests).
func WithClock(now func() time.Time) RecorderOption {
	return func(r *Recorder) {
		if now != nil {
			r.now = now
		}
	}
}

// NewRecorder starts a per-match recorder.
func NewRecorder(game, matchID string, opts ...RecorderOption) *Recorder {
	r := &Recorder{game: game, matchID: matchID, now: time.Now, seats: map[int]*SeatSummary{}}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Record folds one decision into the running per-seat aggregate.
func (r *Recorder) Record(d Decision) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.seats[d.Seat]
	if !ok {
		s = &SeatSummary{Seat: d.Seat, AgentID: d.AgentID, LatencyMinMS: d.LatencyMS, LatencyMaxMS: d.LatencyMS}
		r.seats[d.Seat] = s
		r.order = append(r.order, d.Seat)
	}
	if s.AgentID == "" {
		s.AgentID = d.AgentID
	}
	s.Decisions++
	switch d.Outcome {
	case OutcomeOK:
		s.Legal++
	case OutcomeIllegal:
		s.Illegal++
	case OutcomeTimeout:
		s.Timeouts++
	case OutcomeTransportError:
		s.TransportErrors++
	case OutcomeDisconnected:
		s.Disconnects++
	case OutcomeError:
		s.Errors++
	}
	if d.Outcome.Fallback() {
		s.Fallbacks++
	}
	s.LatencySumMS += d.LatencyMS
	if d.LatencyMS < s.LatencyMinMS {
		s.LatencyMinMS = d.LatencyMS
	}
	if d.LatencyMS > s.LatencyMaxMS {
		s.LatencyMaxMS = d.LatencyMS
	}
	if d.Usage != nil {
		s.PromptTokens += int64(d.Usage.PromptTokens)
		s.CompletionTokens += int64(d.Usage.CompletionTokens)
		s.ReasoningTokens += int64(d.Usage.ReasoningTokens)
		s.CachedTokens += int64(d.Usage.CachedTokens)
		s.CachedWriteTokens += int64(d.Usage.CachedWriteTokens)
		s.TotalTokens += int64(d.Usage.total())
		// Price this move now (uncapped, unlike DecisionLog): prefer the real
		// per-move model, else the seat's manifest model.
		model := d.Usage.Model
		if model == "" {
			model = s.Model
		}
		s.EstimatedCost += pricing.EstimateCost(
			model, d.Usage.PromptTokens, d.Usage.CompletionTokens,
			d.Usage.CachedTokens, d.Usage.CachedWriteTokens, d.Usage.ReasoningTokens,
		)
		s.PricingVersion = pricing.Version
	}
	if len(s.DecisionLog) < maxDecisionLog {
		input, truncated := s.recordInput(d.View)
		// Back-date to the ask. Record runs on the line after the decision returned, so
		// the subtraction is accurate to the cost of a few statements.
		askedAt := r.now().Add(-time.Duration(d.LatencyMS) * time.Millisecond)
		s.DecisionLog = append(s.DecisionLog, DecisionDetail{
			At: askedAt, Round: d.Round, Action: d.Action, Outcome: string(d.Outcome),
			LatencyMS: d.LatencyMS, Rationale: d.Rationale, Usage: d.Usage,
			Input: input, InputTruncated: truncated,
		})
	}
}

// recordInput serializes one turn view under the seat's size budget.
//
// Returns (nil, false) when there was no view to keep and (nil, true) when there was
// one and we chose not to keep it. Never returns an error: instrumentation must not be
// able to fail a match, so a view that will not marshal is simply reported as dropped.
func (s *SeatSummary) recordInput(view any) (json.RawMessage, bool) {
	if view == nil {
		return nil, false
	}
	raw, err := json.Marshal(view)
	if err != nil {
		// A view containing a channel, a func or a NaN. Not a reason to lose the match.
		return nil, true
	}
	if len(raw) > maxInputBytes || s.inputBytes+len(raw) > maxSeatInputBytes {
		return nil, true
	}
	s.inputBytes += len(raw)
	return raw, false
}

// SetAgentMeta records a seat's manifest-declared metadata (version + model
// provider/model), set once at match start — server-authoritative, so provider
// benchmarks don't depend on optional SDK-reported data. Safe on a nil recorder
// or a not-yet-seen seat; empty fields are ignored.
func (r *Recorder) SetAgentMeta(seat int, agentID string, meta AgentMeta) {
	if r == nil || (meta.Version == "" && meta.Provider == "" && meta.Model == "") {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.seats[seat]
	if !ok {
		s = &SeatSummary{Seat: seat, AgentID: agentID}
		r.seats[seat] = s
		r.order = append(r.order, seat)
	}
	if meta.Version != "" {
		s.AgentVersion = meta.Version
	}
	if meta.Provider != "" {
		s.Provider = meta.Provider
	}
	if meta.Model != "" {
		s.Model = meta.Model
	}
}

// SetResult records a seat's match outcome (win/loss/draw), set once at match
// end. Safe on a nil recorder or a seat that never decided (creates the entry so
// the outcome still counts toward matches played).
func (r *Recorder) SetResult(seat int, agentID string, res Result) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.seats[seat]
	if !ok {
		s = &SeatSummary{Seat: seat, AgentID: agentID}
		r.seats[seat] = s
		r.order = append(r.order, seat)
	}
	s.Result = res
}

// Summary returns the immutable per-match aggregate in stable seat order.
func (r *Recorder) Summary() MatchSummary {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := MatchSummary{Game: r.game, MatchID: r.matchID, Seats: make([]SeatSummary, 0, len(r.order))}
	for _, seat := range r.order {
		out.Seats = append(out.Seats, *r.seats[seat])
	}
	return out
}

// Empty reports whether any decision was recorded (used to skip emitting a no-op
// summary for a match that never asked an agent to move).
func (r *Recorder) Empty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.order) == 0
}
