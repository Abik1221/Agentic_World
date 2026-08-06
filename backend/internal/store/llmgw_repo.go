package store

import (
	"context"

	"github.com/agent-arena/arena/internal/llmgw"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LLMGatewayRepo persists what the gateway observed. Implements llmgw.Recorder.
//
// Every write here is off the response path — the gateway records after the bytes have
// reached the agent — so this may be slow without costing an agent a turn. What it must
// never do is fail loudly: a bookkeeping problem must not become the reason a match ends.
type LLMGatewayRepo struct{ db *pgxpool.Pool }

func NewLLMGatewayRepo(db *pgxpool.Pool) *LLMGatewayRepo { return &LLMGatewayRepo{db: db} }

// RecordCall stores one observed call.
//
// Unbound calls are stored too. The bound/total ratio is the COVERAGE a verified board has
// to publish, and without the denominator a cost-per-win metric is perverse: an agent that
// routes 5% of its calls and does the rest elsewhere looks like the cheapest on the
// platform. Discarding unbound rows would hide exactly the attack.
//
// A missing agent is skipped rather than erroring: the agent id came from an authenticated
// principal, so this can only happen if the account was deleted mid-match, and inventing a
// row for a nonexistent agent is worse than dropping one call.
func (r *LLMGatewayRepo) RecordCall(ctx context.Context, c llmgw.Call) error {
	// match_id and round go in as NULL rather than '' / 0 when absent. A call outside any
	// match is a real thing worth costing, and storing round 0 would make it look like a
	// decision that never happened.
	var matchID *string
	var round *int
	if c.MatchID != "" {
		matchID = &c.MatchID
		rd := c.Round
		round = &rd
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_model_calls (
		     agent_id, match_id, round, bound, provider, model,
		     prompt_tokens, completion_tokens, cached_read_tokens, cached_write_tokens,
		     reasoning_tokens, latency_ms, status, streamed)
		 SELECT a.id, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
		   FROM agents a WHERE a.public_id = $1`,
		c.AgentPublicID, matchID, round, c.Bound, c.Provider, c.Model,
		c.PromptTokens, c.CompletionTokens, c.CachedReadTokens, c.CachedWriteTokens,
		c.ReasoningTokens, c.LatencyMS, c.Status, c.Streamed)
	return err
}

// BindDecision marks (match, agent, round) as proven LLM-backed.
//
// Reuses the same table the ranked integrity gate reads, so "verified for the leaderboard"
// and "provably LLM-backed for settlement" are one fact rather than two that can disagree.
// Idempotent: an agent may legitimately make several calls for one decision (a best-of-N
// sample, a tool loop), and each carries the same valid proof.
func (r *LLMGatewayRepo) BindDecision(ctx context.Context, matchID, agentPublicID string, round int) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_match_bound_decisions (match_id, agent_id, round)
		 SELECT $1, a.id, $3 FROM agents a WHERE a.public_id = $2
		 ON CONFLICT (match_id, agent_id, round) DO NOTHING`,
		matchID, agentPublicID, round)
	return err
}

// CoverageFor computes verified coverage for one agent over a match, or over all its play
// when matchID is empty.
//
// Counts DISTINCT decisions, not calls. An agent that made forty calls for one decision has
// covered one decision, and counting calls would let volume manufacture coverage.
func (r *LLMGatewayRepo) CoverageFor(ctx context.Context, agentPublicID, matchID string) (llmgw.Coverage, error) {
	out := llmgw.Coverage{AgentPublicID: agentPublicID}
	err := r.db.QueryRow(ctx,
		`WITH d AS (
		   SELECT dd.match_id, dd.seq
		     FROM agent_match_decisions dd
		     JOIN agents a ON a.id = dd.agent_id
		    WHERE a.public_id = $1 AND ($2 = '' OR dd.match_id = $2)
		 ), b AS (
		   SELECT DISTINCT bd.match_id, bd.round
		     FROM agent_match_bound_decisions bd
		     JOIN agents a ON a.id = bd.agent_id
		    WHERE a.public_id = $1 AND ($2 = '' OR bd.match_id = $2)
		 )
		 SELECT (SELECT count(*) FROM d), (SELECT count(*) FROM b)`,
		agentPublicID, matchID).Scan(&out.Decisions, &out.BoundDecisions)
	if err != nil {
		return out, err
	}
	if out.Decisions > 0 {
		// Clamped: an agent may bind a round the decision log has no row for (a call for a
		// turn it never answered), which could otherwise report coverage above 100% and
		// make the figure look broken rather than conservative.
		out.Coverage = float64(min(out.BoundDecisions, out.Decisions)) / float64(out.Decisions)
	}
	return out, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// RecordVerifiedCost accumulates one observed call into the match's verified spend.
//
// Delegates to the same statement the older gateway used, so the two cannot disagree about how
// spend accumulates while both exist — and so retiring that gateway changes which code CALLS
// this, not what it does.
func (r *LLMGatewayRepo) RecordVerifiedCost(ctx context.Context, c llmgw.VerifiedCost) error {
	return NewPIndexRepo(r.db).RecordVerifiedCost(ctx, VerifiedCall{
		MatchID: c.MatchID, AgentPublicID: c.AgentPublicID, CostUSD: c.CostUSD,
		Provider: c.Provider, Model: c.Model,
		PromptTokens: int64(c.PromptTokens), CompletionTokens: int64(c.CompletionTokens),
		TotalTokens: int64(c.TotalTokens),
	})
}
