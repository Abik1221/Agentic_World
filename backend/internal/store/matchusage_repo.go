package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MatchUsageRepo answers "did my telemetry actually land?" for one match.
//
// Every piece below was already recorded and none of it was reachable by the
// developer who produced it. `pyyol replay` is the only read path in the CLI and it
// carries the game, not the metering, so an agent author could not confirm their
// tokens were captured, their cost was measured, or their decisions counted as
// LLM-backed — for the very features the Verified badge and ranked validity depend
// on. The feedback loop was: ship, and find out when a ranked match is voided.
type MatchUsageRepo struct{ db *pgxpool.Pool }

func NewMatchUsageRepo(db *pgxpool.Pool) *MatchUsageRepo { return &MatchUsageRepo{db: db} }

// OwnedBy reports whether agentPublicID belongs to userPublicID. Used to keep one
// developer from reading another's token spend and cost, which would leak the shape
// of their strategy budget.
// The join column is owner_user_id. It was `a.user_id`, which does not exist on agents
// and never has (migration 0002 creates owner_user_id) — so this query failed with
// `column a.user_id does not exist` on EVERY call, and GET /v1/matches/{id}/usage,
// the only surface that ever exposed tokens, cost and fallbacks to the developer who
// produced them, answered 500 for its entire life. Nothing in the product read it, so
// nothing reported the break.
func (r *MatchUsageRepo) OwnedBy(ctx context.Context, userPublicID, agentPublicID string) (bool, error) {
	var n int
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM agents a JOIN users u ON u.id = a.owner_user_id
		  WHERE a.public_id = $1 AND u.public_id = $2`,
		agentPublicID, userPublicID).Scan(&n)
	return n > 0, err
}

// MatchUsage is one agent's metering for one match.
type MatchUsage struct {
	MatchID   string `json:"match_id"`
	AgentID   string `json:"agent_id"`
	Decisions int    `json:"decisions"`
	Legal     int    `json:"legal"`
	// Fallbacks are moves the ENGINE played because the agent was late, illegal or
	// unreachable. Surfaced prominently because it is the number that quietly ruins a
	// win rate and never appears anywhere else.
	Fallbacks    int     `json:"fallbacks"`
	AvgLatencyMS int64   `json:"avg_latency_ms"`
	Tokens       int64   `json:"tokens"`
	SelfReported float64 `json:"self_reported_cost_usd"`
	// VerifiedCost and VerifiedCalls come from the GATEWAY — server-observed, so they
	// are the ones that count. Zero here with non-zero tokens above means the agent is
	// reporting usage but not routing through route(), i.e. it is not verified.
	VerifiedCost  float64 `json:"verified_cost_usd"`
	VerifiedCalls int     `json:"verified_calls"`
	// BoundDecisions is how many decisions carried a proof token minted for that exact
	// turn — the measure ranked integrity actually uses.
	BoundDecisions int `json:"bound_decisions"`
}

// ForAgent returns the metering for (match, agent). Missing rows read as zeroes
// rather than an error: a match with no telemetry is a real answer, and the honest
// one — it tells the developer nothing landed.
func (r *MatchUsageRepo) ForAgent(ctx context.Context, matchID, agentPublicID string) (MatchUsage, error) {
	u := MatchUsage{MatchID: matchID, AgentID: agentPublicID}
	err := r.db.QueryRow(ctx, `
		SELECT
		  COALESCE(b.decisions, 0), COALESCE(b.legal, 0), COALESCE(b.fallbacks, 0),
		  CASE WHEN COALESCE(b.decisions,0) > 0
		       THEN COALESCE(b.latency_sum_ms,0) / b.decisions ELSE 0 END,
		  COALESCE(b.tokens, 0), COALESCE(b.estimated_cost, 0),
		  COALESCE(v.verified_cost, 0), COALESCE(v.calls, 0),
		  COALESCE((SELECT COUNT(*) FROM agent_match_bound_decisions d
		             WHERE d.match_id = $1 AND d.agent_id = a.id), 0)
		FROM agents a
		LEFT JOIN agent_match_benchmark b     ON b.match_id = $1 AND b.agent_id = a.id
		LEFT JOIN agent_match_verified_cost v ON v.match_id = $1 AND v.agent_id = a.id
		WHERE a.public_id = $2`,
		matchID, agentPublicID).Scan(
		&u.Decisions, &u.Legal, &u.Fallbacks, &u.AvgLatencyMS,
		&u.Tokens, &u.SelfReported, &u.VerifiedCost, &u.VerifiedCalls, &u.BoundDecisions)
	return u, err
}
