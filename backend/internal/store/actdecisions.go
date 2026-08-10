package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/agent-arena/arena/internal/benchmark"
)

// Instrumentation for the PULL path — agents that play by calling Act rather than being driven.
//
// # Why this exists
//
// Per-match benchmark instrumentation lived entirely inside the platform's drive loops:
// benchmark.NewRecorder appears only in the four drivers, and each builds a per-match Recorder
// in memory, folds decisions into it, and Flushes a summary at the end. An agent that polls
// State and posts Act touches none of that, so it produced no benchmark fact, no decision log
// and no board presence — in any game.
//
// The effect was hidden by the shape of the arenas. Goofspiel has TWO instrumented paths and
// its ranked play is always platform-driven, so it looked fine. Monopoly and Mafia have one
// each and the pull path is the common one: 2067 finished rated Monopoly matches carried 8.4M
// match_events and zero match.benchmark events, so every figure the platform published was a
// Goofspiel figure while presenting itself as describing the platform.
// See OBSERVABILITY_COVERAGE_GAP.md.
//
// # Why durable rows rather than an in-memory recorder
//
// Act is a stateless per-request handler, so a Recorder has nowhere to live across calls. The
// alternative — a match-keyed recorder held in the service — would mean mutable per-match state
// that has to be reaped on abandonment and made correct across instances, which is exactly the
// state this codebase keeps in Postgres rather than memory. Writing each decision durably as it
// happens also means a match survives the instance that was serving it dying mid-game.
//
// # Why agent_match_decisions rather than a staging table
//
// It already has precisely this shape and is already what every consumer reads. A staging table
// would add a second definition of "a decision" that could drift from the one the drivers
// produce; writing the same rows means the two paths cannot disagree about what a decision is.
// Both writes are upserts on their natural keys, so a match that somehow goes through BOTH a
// driver and Act converges instead of duplicating.

// ActDecision is one decision an agent made by calling Act.
//
// Deliberately the same fields the drivers record, so a row written here is indistinguishable
// from one written by a drive loop. A board must not be able to tell which transport produced a
// decision, because the transport is not a property of the agent's play.
type ActDecision struct {
	MatchID       string
	AgentPublicID string
	Game          string
	// Seq is the agent's own decision counter within the match, 0-based. It is the upsert key
	// alongside (match, agent), so a retried Act must reuse its seq rather than appending a
	// duplicate decision.
	Seq       int
	Round     int
	Action    string
	Outcome   string
	LatencyMS int64
	Rationale string
	// Usage is the SDK-reported model usage for this decision, if any.
	Usage *benchmark.TokenUsage
	// InputJSON is the view the agent was handed. Already size-capped by the caller: the
	// decision inspector needs the state a move was made against, and a Monopoly board is the
	// largest view of the three arenas.
	InputJSON []byte
	StartedAt time.Time
}

// RecordActDecision persists one pull-path decision.
//
// Idempotent on (match_id, agent_id, seq): Act can be retried by a client or replayed by a
// proxy, and a retry must refresh the row rather than invent a second decision that never
// happened. Model attribution and the rationale are never blanked by a later write that lacks
// them, matching the driver path's rule — a replay that lost its usage block must not erase
// what an earlier one recorded.
func (r *PIndexRepo) RecordActDecision(ctx context.Context, d ActDecision) error {
	if d.MatchID == "" || d.AgentPublicID == "" {
		return nil
	}
	var (
		provider, model                                    string
		prompt, completion, reasoning, cached, cachedWrite int
		total                                              int
		cost                                               float64
		scaffold, scaffoldIssue                            string
		scaffoldUnstable                                   bool
	)
	if u := d.Usage; u != nil {
		provider, model = u.Provider, u.Model
		prompt, completion, reasoning = u.PromptTokens, u.CompletionTokens, u.ReasoningTokens
		cached, cachedWrite = u.CachedTokens, u.CachedWriteTokens
		total = u.TotalTokens
		if total == 0 {
			total = prompt + completion + reasoning
		}
		scaffold, scaffoldIssue, scaffoldUnstable = u.Scaffold, u.ScaffoldIssue, u.ScaffoldUnstable
		cost = actDecisionCost(model, u)
	}

	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_match_decisions (
		     match_id, agent_id, seq, round, action, outcome, latency_ms, rationale,
		     provider, model, prompt_tokens, completion_tokens, reasoning_tokens,
		     cached_tokens, cached_write_tokens, total_tokens, estimated_cost,
		     scaffold, scaffold_unstable, scaffold_issue,
		     input_json, input_truncated, started_at)
		 SELECT $1, a.id, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17,
		        $18, $19, $20, $21::jsonb, false, $22
		   FROM agents a WHERE a.public_id = $2
		 ON CONFLICT (match_id, agent_id, seq) DO UPDATE SET
		   round = EXCLUDED.round, action = EXCLUDED.action, outcome = EXCLUDED.outcome,
		   latency_ms = EXCLUDED.latency_ms,
		   rationale = COALESCE(NULLIF(EXCLUDED.rationale,''), agent_match_decisions.rationale),
		   provider = COALESCE(NULLIF(EXCLUDED.provider,''), agent_match_decisions.provider),
		   model = COALESCE(NULLIF(EXCLUDED.model,''), agent_match_decisions.model),
		   prompt_tokens = EXCLUDED.prompt_tokens,
		   completion_tokens = EXCLUDED.completion_tokens,
		   reasoning_tokens = EXCLUDED.reasoning_tokens,
		   cached_tokens = EXCLUDED.cached_tokens,
		   cached_write_tokens = EXCLUDED.cached_write_tokens,
		   total_tokens = EXCLUDED.total_tokens,
		   estimated_cost = EXCLUDED.estimated_cost,
		   scaffold = COALESCE(NULLIF(EXCLUDED.scaffold,''), agent_match_decisions.scaffold),
		   scaffold_unstable = EXCLUDED.scaffold_unstable OR agent_match_decisions.scaffold_unstable,
		   scaffold_issue = EXCLUDED.scaffold_issue,
		   input_json = COALESCE(EXCLUDED.input_json, agent_match_decisions.input_json),
		   started_at = COALESCE(EXCLUDED.started_at, agent_match_decisions.started_at)`,
		d.MatchID, d.AgentPublicID, d.Seq, d.Round, d.Action, d.Outcome, d.LatencyMS, d.Rationale,
		provider, model, prompt, completion, reasoning, cached, cachedWrite, total, cost,
		scaffold, scaffoldUnstable, scaffoldIssue,
		inputOrNil(d.InputJSON), timeOrNil(d.StartedAt))
	return err
}

// actDecisionCost prices one decision with the same versioned table the drivers use.
//
// Priced HERE rather than left to the aggregation because cost depends on the model, and the
// model is a per-decision fact: an agent that switched models mid-match would otherwise have
// every decision priced at whichever model it finished on.
func actDecisionCost(model string, u *benchmark.TokenUsage) float64 {
	if u == nil {
		return 0
	}
	return benchmark.PriceUsage(model, u)
}

// AggregateSeatBenchmark builds the per-seat benchmark facts for a finished match FROM the
// decision rows already persisted, and upserts them.
//
// This is the match-end half of the pull path. The drivers get their seat aggregate by Flushing
// an in-memory Recorder; a pull-path match has no Recorder, so the aggregate is derived from the
// durable rows instead. Same destination table, so no consumer can tell the two apart.
//
// results maps agent public id -> "win" | "loss" | "draw". The outcome cannot come from the
// decision log — a decision knows whether it was legal, not whether the match was won — so the
// caller, which knows the match result, supplies it. A seat missing from the map still gets a
// row, with an empty result: "we recorded its play but not its outcome" is a real state and
// silently dropping the seat would understate the match.
func (r *PIndexRepo) AggregateSeatBenchmark(ctx context.Context, matchID, game string, results map[string]string) error {
	if matchID == "" {
		return nil
	}
	resultJSON, err := json.Marshal(results)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx,
		`WITH agg AS (
		   SELECT d.agent_id, a.public_id,
		          COUNT(*)::int                                                    AS decisions,
		          COUNT(*) FILTER (WHERE d.outcome IN ('ok','legal'))::int          AS legal,
		          COUNT(*) FILTER (WHERE d.outcome = 'fallback')::int               AS fallbacks,
		          COUNT(*) FILTER (WHERE d.outcome = 'illegal')::int                AS illegal,
		          COUNT(*) FILTER (WHERE d.outcome = 'timeout')::int                AS timeouts,
		          COUNT(*) FILTER (WHERE d.outcome = 'transport_error')::int        AS transport_errors,
		          COALESCE(SUM(d.latency_ms),0)::bigint                            AS latency_sum_ms,
		          COALESCE(MIN(NULLIF(d.latency_ms,0)),0)::bigint                  AS latency_min_ms,
		          COALESCE(MAX(d.latency_ms),0)::bigint                            AS latency_max_ms,
		          COALESCE(SUM(d.total_tokens),0)::bigint                          AS tokens,
		          COALESCE(SUM(d.prompt_tokens),0)::bigint                         AS prompt_tokens,
		          COALESCE(SUM(d.completion_tokens),0)::bigint                     AS completion_tokens,
		          COALESCE(SUM(d.reasoning_tokens),0)::bigint                      AS reasoning_tokens,
		          COALESCE(SUM(d.cached_tokens),0)::bigint                         AS cached_tokens,
		          COALESCE(SUM(d.estimated_cost),0)::double precision              AS estimated_cost,
		          -- The model this seat ACTUALLY ran, taken from its LAST attributed decision:
		          -- an agent that switched mid-match finished on the later one. MAX over a
		          -- (seq, model) tuple picks the model at the highest seq that had one, rather
		          -- than the alphabetically largest model.
		          (ARRAY_AGG(d.provider ORDER BY d.seq DESC) FILTER (WHERE d.provider <> ''))[1] AS obs_provider,
		          (ARRAY_AGG(d.model    ORDER BY d.seq DESC) FILTER (WHERE d.model    <> ''))[1] AS obs_model
		     FROM agent_match_decisions d
		     JOIN agents a ON a.id = d.agent_id
		    WHERE d.match_id = $1
		    GROUP BY d.agent_id, a.public_id
		 )
		 INSERT INTO agent_match_benchmark (
		     match_id, agent_id, game, result, decisions, legal, fallbacks, illegal,
		     timeouts, transport_errors, latency_sum_ms, latency_min_ms, latency_max_ms,
		     tokens, prompt_tokens, completion_tokens, reasoning_tokens, cached_tokens,
		     estimated_cost, observed_provider, observed_model, updated_at)
		 SELECT $1, agg.agent_id, $2,
		        COALESCE($3::jsonb ->> agg.public_id, ''),
		        agg.decisions, agg.legal, agg.fallbacks, agg.illegal, agg.timeouts,
		        agg.transport_errors, agg.latency_sum_ms, agg.latency_min_ms, agg.latency_max_ms,
		        agg.tokens, agg.prompt_tokens, agg.completion_tokens, agg.reasoning_tokens,
		        agg.cached_tokens, agg.estimated_cost,
		        COALESCE(agg.obs_provider,''), COALESCE(agg.obs_model,''), now()
		   FROM agg
		 ON CONFLICT (match_id, agent_id) DO UPDATE SET
		   game = EXCLUDED.game,
		   -- Never blank a result we already have: the drivers may have written one first, and
		   -- this aggregation only knows what its caller passed.
		   result = COALESCE(NULLIF(EXCLUDED.result,''), agent_match_benchmark.result),
		   decisions = EXCLUDED.decisions, legal = EXCLUDED.legal,
		   fallbacks = EXCLUDED.fallbacks, illegal = EXCLUDED.illegal,
		   timeouts = EXCLUDED.timeouts, transport_errors = EXCLUDED.transport_errors,
		   latency_sum_ms = EXCLUDED.latency_sum_ms,
		   latency_min_ms = EXCLUDED.latency_min_ms,
		   latency_max_ms = EXCLUDED.latency_max_ms,
		   tokens = EXCLUDED.tokens, prompt_tokens = EXCLUDED.prompt_tokens,
		   completion_tokens = EXCLUDED.completion_tokens,
		   reasoning_tokens = EXCLUDED.reasoning_tokens,
		   cached_tokens = EXCLUDED.cached_tokens,
		   estimated_cost = EXCLUDED.estimated_cost,
		   observed_provider = COALESCE(NULLIF(EXCLUDED.observed_provider,''), agent_match_benchmark.observed_provider),
		   observed_model = COALESCE(NULLIF(EXCLUDED.observed_model,''), agent_match_benchmark.observed_model),
		   updated_at = now()`,
		matchID, game, string(resultJSON))
	return err
}
