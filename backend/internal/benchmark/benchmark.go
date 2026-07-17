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

import "sync"

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
}

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

	mu    sync.Mutex
	seats map[int]*SeatSummary
	order []int // seat insertion order, for a stable Summary
}

// NewRecorder starts a per-match recorder.
func NewRecorder(game, matchID string) *Recorder {
	return &Recorder{game: game, matchID: matchID, seats: map[int]*SeatSummary{}}
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
