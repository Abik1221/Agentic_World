package devtrace

import (
	"context"
	"net/http"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
)

// ONE AGENT, ACROSS EVERY MATCH — the view a developer works from between games.
//
// The match trace answers "what went wrong in that game". This answers the question you
// ask before you change any code: *what does my agent get wrong REPEATEDLY*. A single bad
// match is an anecdote; the same failure in the same phase across forty matches is a bug
// with an address.
//
// Everything here is derived from the per-decision record (agent_match_decisions), so the
// numbers reconcile against the individual traces a developer can open to check them.
//
// ISOLATION. This is an aggregate, which is exactly where scoping goes wrong: a GROUP BY
// that forgets its WHERE returns a summary of the whole platform and looks perfectly
// plausible. The owned-agent list is applied inside the aggregating query, and the service
// resolves the requested agent through the same `actors` gate as every other trace path —
// an agent the caller does not own resolves to nothing rather than to an error that would
// confirm it exists.

// FailureCount is one failure cause and how often it happened.
type FailureCount struct {
	// Outcome is the engine's classification: illegal_move, timeout, transport_error,
	// disconnected, error.
	Outcome string `json:"outcome"`
	Count   int    `json:"count"`
	// Share of ALL decisions, so a big number on a busy agent is readable next to a
	// small one on a quiet agent.
	Share float64 `json:"share"`
}

// ArenaTelemetry is one arena's slice of an agent's record. Failure modes are
// arena-specific — a Mafia timeout during a discussion phase and a Goofspiel timeout on a
// sealed bid are different bugs — so the split is never averaged away.
type ArenaTelemetry struct {
	Game      string  `json:"game"`
	Matches   int     `json:"matches"`
	Decisions int     `json:"decisions"`
	Failures  int     `json:"failures"`
	LegalRate float64 `json:"legal_rate"`
	P50Ms     int     `json:"p50_ms"`
	P95Ms     int     `json:"p95_ms"`
	Tokens    int64   `json:"tokens"`
	CostUSD   float64 `json:"cost_usd"`
}

// RoundHotspot is a round/phase number that fails disproportionately — the strongest
// single signal this page produces, because it points at a specific place in the agent's
// own logic rather than at a rate.
type RoundHotspot struct {
	Round     int     `json:"round"`
	Game      string  `json:"game"`
	Decisions int     `json:"decisions"`
	Failures  int     `json:"failures"`
	Rate      float64 `json:"rate"`
}

// WorstDecision is a single decision worth opening, with the ids needed to link straight
// to its inspector page.
type WorstDecision struct {
	MatchID   string     `json:"match_id"`
	Seq       int        `json:"seq"`
	Game      string     `json:"game"`
	Round     int        `json:"round"`
	Outcome   string     `json:"outcome"`
	LatencyMS int64      `json:"latency_ms"`
	Action    string     `json:"action,omitempty"`
	Rationale string     `json:"rationale,omitempty"`
	At        *time.Time `json:"at,omitempty"`
}

// AgentTelemetry is the whole page.
type AgentTelemetry struct {
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name,omitempty"`
	Days      int    `json:"days"`

	Matches   int `json:"matches"`
	Decisions int `json:"decisions"`
	Failures  int `json:"failures"`

	LegalRate    float64 `json:"legal_rate"`
	FallbackRate float64 `json:"fallback_rate"`

	// Latency percentiles, not a mean: a mean hides the tail, and the tail is what times
	// out under load. P99 next to P50 is the difference between "my agent is slow" and
	// "my agent is fine except when it isn't".
	P50Ms int `json:"p50_ms"`
	P95Ms int `json:"p95_ms"`
	P99Ms int `json:"p99_ms"`
	MaxMs int `json:"max_ms"`

	Tokens            int64   `json:"tokens"`
	CostUSD           float64 `json:"cost_usd"`
	TokensPerDecision float64 `json:"tokens_per_decision"`

	// ReasonedShare is how many decisions carried a rationale. Low values are not a
	// defect — they mean this page and the traces under it can explain less.
	ReasonedShare float64 `json:"reasoned_share"`

	FailuresByCause []FailureCount   `json:"failures_by_cause"`
	Arenas          []ArenaTelemetry `json:"arenas"`
	Hotspots        []RoundHotspot   `json:"hotspots"`
	Slowest         []WorstDecision  `json:"slowest"`
	RecentFailures  []WorstDecision  `json:"recent_failures"`
}

// TelemetryRepo is the aggregate read port. Separate from MatchRepo because these are
// cross-match rollups, not a scan of one game.
type TelemetryRepo interface {
	// AgentTelemetry rolls up the per-decision record for these agents over a window.
	// MUST scope to agentPublicIDs inside the aggregating query.
	AgentTelemetry(ctx context.Context, agentPublicIDs []string, since time.Time) (AgentTelemetry, error)
	// AgentWorstDecisions returns the slowest decisions and the most recent failures.
	AgentWorstDecisions(ctx context.Context, agentPublicIDs []string, since time.Time, limit int) (slowest, failures []WorstDecision, err error)
}

// SetTelemetryRepo wires the aggregate source.
func (s *Service) SetTelemetryRepo(r TelemetryRepo) { s.telemetry = r }

// maxTelemetryDays bounds the window. A developer asking for "everything" on a busy agent
// would otherwise scan the whole table for a page they read in ten seconds.
const maxTelemetryDays = 90

// AgentTelemetry assembles the cross-match view for one of the caller's agents.
//
// agentPublicID may be empty, meaning "all of my agents" — useful for a developer with
// one agent, and honest for one with several because the arena and hotspot splits keep it
// interpretable.
func (s *Service) AgentTelemetry(ctx context.Context, userPublicID, agentPublicID string, days int) (AgentTelemetry, error) {
	if s.telemetry == nil {
		return AgentTelemetry{}, httpx.NewError(http.StatusServiceUnavailable, "traces_unconfigured",
			"Agent telemetry is not enabled in this environment.")
	}
	if days <= 0 {
		days = 30
	}
	if days > maxTelemetryDays {
		days = maxTelemetryDays
	}

	// THE GATE — the same one every other trace path uses. An agent the caller does not
	// own resolves to an empty list, and an empty list is a filter that matches nothing.
	owned, err := s.actors(ctx, userPublicID, agentPublicID)
	if err != nil {
		return AgentTelemetry{}, err
	}
	if len(owned) == 0 {
		// Not an authorisation error: consistent with the rest of the trace surface, and
		// it does not confirm whether the requested agent id exists.
		return AgentTelemetry{AgentID: agentPublicID, Days: days}, nil
	}

	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	out, err := s.telemetry.AgentTelemetry(ctx, owned, since)
	if err != nil {
		return AgentTelemetry{}, err
	}
	slowest, failures, err := s.telemetry.AgentWorstDecisions(ctx, owned, since, 10)
	if err != nil {
		return AgentTelemetry{}, err
	}

	out.AgentID, out.Days = agentPublicID, days
	out.Slowest, out.RecentFailures = slowest, failures
	if out.Decisions > 0 {
		d := float64(out.Decisions)
		out.FallbackRate = float64(out.Failures) / d
		out.TokensPerDecision = float64(out.Tokens) / d
		for i := range out.FailuresByCause {
			out.FailuresByCause[i].Share = float64(out.FailuresByCause[i].Count) / d
		}
	}
	return out, nil
}
