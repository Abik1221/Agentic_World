package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/events"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/pindex"
	"github.com/agent-arena/arena/internal/skill"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PIndexRepo is the pgx implementation of pindex.Repo. It assembles a developer's
// scoring inputs (best agent per arena, match totals, opponent strength) and
// persists computed results transactionally with the pindex.updated event.
type PIndexRepo struct{ db *pgxpool.Pool }

func NewPIndexRepo(db *pgxpool.Pool) *PIndexRepo { return &PIndexRepo{db: db} }

var _ pindex.Repo = (*PIndexRepo)(nil)

// Public sinks filter agents by an ALLOWLIST (kind = 'external'), never by excluding a
// kind they happen to know about.
//
// They used to read `kind <> 'house'`, which means "everything except the one thing I
// thought of". That is safe only while exactly two kinds exist. The moment a third is added
// — the platform's own benchmark harness is the immediate case — every one of these queries
// starts including it, silently: no error, nothing odd in review, just the platform's own
// agents appearing on public developer boards as though they were developers.
//
// An allowlist inverts the default. A new kind is invisible to the public surfaces until
// somebody deliberately lists it, which is the same fail-closed shape as mustCertify
// returning "must certify" when the roster is nil.
//
// Admin repos deliberately do NOT do this: a console that cannot see the agents it operates
// is broken. The split is public-facing vs operator-facing, not a blanket rule.

func (r *PIndexRepo) ActiveConfig(ctx context.Context) (pindex.Config, error) {
	var version int
	var params []byte
	err := r.db.QueryRow(ctx,
		`SELECT version, params FROM pindex_config WHERE active ORDER BY version DESC LIMIT 1`).
		Scan(&version, &params)
	if err != nil {
		return pindex.Config{}, err
	}
	return pindex.ParseConfig(version, params)
}

func (r *PIndexRepo) Inputs(ctx context.Context, userPublicID string, season int, asOf time.Time) (pindex.DeveloperInputs, error) {
	in := pindex.DeveloperInputs{UserPublicID: userPublicID, Season: season, AsOf: asOf}

	var uid int64
	if err := r.db.QueryRow(ctx, `SELECT id FROM users WHERE public_id = $1`, userPublicID).Scan(&uid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return in, nil // unknown developer ⇒ empty inputs (scores 0)
		}
		return in, err
	}

	// Per-arena standing: the developer's BEST agent (highest displayed rating) in
	// each arena this season.
	rows, err := r.db.Query(ctx,
		`SELECT DISTINCT ON (r.game) r.game, r.elo, r.rd, r.sigma, r.algo, (r.wins + r.losses + r.ties)
		 FROM ratings r JOIN agents a ON a.id = r.agent_id
		 WHERE a.owner_user_id = $1 AND r.season = $2 AND a.kind = 'external'
		 ORDER BY r.game, r.elo DESC`, uid, season)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var a pindex.ArenaInput
		if err := rows.Scan(&a.Game, &a.Rating, &a.RD, &a.Sigma, &a.Algo, &a.Matches); err != nil {
			rows.Close()
			return in, err
		}
		in.Arenas = append(in.Arenas, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}

	// Activity totals: distinct rated matches + distinct arenas + last-match time.
	// Anti-abuse: matches carrying an active fraud flag (farming, collusion,
	// same-owner dumping, bot timing) are EXCLUDED from the activity + difficulty
	// inputs, so manipulation can't inflate the P-Index.
	var last *time.Time
	if err := r.db.QueryRow(ctx,
		`SELECT COUNT(DISTINCT mrc.match_id), COUNT(DISTINCT mrc.game), MAX(mrc.created_at)
		 FROM match_rating_changes mrc JOIN agents a ON a.id = mrc.agent_id
		 WHERE a.owner_user_id = $1 AND mrc.season = $2 AND a.kind = 'external'
		   AND NOT EXISTS (SELECT 1 FROM fraud_flags f WHERE f.match_id = mrc.match_id AND f.active)`,
		uid, season).Scan(&in.TotalMatches, &in.DistinctArenas, &last); err != nil {
		return in, err
	}
	if last != nil {
		in.LastMatchAt = *last
	}

	// Difficulty: mean opponent rating faced (at match time), overall and in wins.
	// Opponents that are the developer's OWN agents are excluded.
	if err := r.db.QueryRow(ctx,
		`SELECT COALESCE(AVG(opp.rating_before), 0),
		        COALESCE(AVG(opp.rating_before) FILTER (WHERE self.rank_in_match < opp.rank_in_match), 0)
		 FROM match_rating_changes self
		 JOIN agents sa ON sa.id = self.agent_id AND sa.owner_user_id = $1 AND sa.kind = 'external'
		 JOIN match_rating_changes opp ON opp.match_id = self.match_id AND opp.agent_id <> self.agent_id
		 JOIN agents oa ON oa.id = opp.agent_id AND oa.owner_user_id <> $1
		 WHERE self.season = $2
		   AND NOT EXISTS (SELECT 1 FROM fraud_flags f WHERE f.match_id = self.match_id AND f.active)`,
		uid, season).Scan(&in.AvgOppRating, &in.AvgOppRatingOnWin); err != nil {
		return in, err
	}

	// Intelligence: engine-measured decision quality (legal / fallback / latency)
	// across the developer's RANKED matches this season. Joined through matches +
	// match_rating_changes so it reuses the exact owner/season/fraud scoping;
	// non-ranked matches (no rating row) never join and are excluded.
	var dec, legal, fb, latSum int64
	if err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(amb.decisions),0), COALESCE(SUM(amb.legal),0),
		        COALESCE(SUM(amb.fallbacks),0), COALESCE(SUM(amb.latency_sum_ms),0)
		 FROM agent_match_benchmark amb
		 JOIN matches m ON m.public_id = amb.match_id
		 JOIN match_rating_changes mrc ON mrc.match_id = m.id AND mrc.agent_id = amb.agent_id
		 JOIN agents a ON a.id = amb.agent_id AND a.owner_user_id = $1 AND a.kind = 'external'
		 WHERE mrc.season = $2
		   AND NOT EXISTS (SELECT 1 FROM fraud_flags f WHERE f.match_id = mrc.match_id AND f.active)`,
		uid, season).Scan(&dec, &legal, &fb, &latSum); err != nil {
		return in, err
	}
	if dec > 0 {
		in.BenchDecisions = int(dec)
		in.LegalRate = float64(legal) / float64(dec)
		in.FallbackRate = float64(fb) / float64(dec)
		in.AvgLatencyMS = float64(latSum) / float64(dec)
	}

	// Skill: DECISION QUALITY, from the per-decision scores internal/skill wrote.
	//
	// Scoped exactly like the intelligence rollup above — same owner, same season, same
	// fraud exclusion, same ranked-only join — so the two dimensions describe the same set
	// of matches and cannot disagree about which games counted.
	//
	// `skill_regret IS NOT NULL` is the load-bearing predicate. A row can be stamped with
	// a scorer version and still hold NULL when the decision was not scorable (a game with
	// no scorer, a malformed view). Counting those as zero regret would hand every
	// Monopoly agent a perfect record, since Monopoly has no scorer yet.
	//
	// The version filter keeps one average from mixing verdicts produced by two different
	// scorers, which would compare agents against different yardsticks.
	var skillN int64
	var regretSum, blunders float64
	if err := r.db.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(SUM(d.skill_regret),0),
		        COALESCE(SUM(CASE WHEN d.skill_regret > $3 THEN 1 ELSE 0 END),0)
		   FROM agent_match_decisions d
		   JOIN matches m ON m.public_id = d.match_id
		   JOIN match_rating_changes mrc ON mrc.match_id = m.id AND mrc.agent_id = d.agent_id
		   JOIN agents a ON a.id = d.agent_id AND a.owner_user_id = $1 AND a.kind = 'external'
		  WHERE mrc.season = $2
		    AND d.skill_regret IS NOT NULL
		    AND d.skill_scorer_version = $4
		    AND NOT EXISTS (SELECT 1 FROM fraud_flags f WHERE f.match_id = mrc.match_id AND f.active)`,
		uid, season, skill.BlunderThreshold, skill.ScorerVersion).Scan(&skillN, &regretSum, &blunders); err != nil {
		return in, err
	}
	if skillN > 0 {
		in.SkillDecisions = int(skillN)
		in.SkillQuality = 1 - regretSum/float64(skillN)
		in.SkillBlunderRate = blunders / float64(skillN)
	}

	return in, nil
}

// MatchBenchmarkFact is one seat's complete per-match benchmark record: decision
// quality, the failure taxonomy behind it, LLM economics, and which model played.
//
// A struct rather than a parameter list because this reached fourteen positional
// arguments, at which point a caller can transpose two int64s (tokens and latency,
// say) and still compile — while the board silently reports a model that burned
// 40,000 ms of tokens.
type MatchBenchmarkFact struct {
	MatchID       string
	AgentPublicID string
	Game          string
	Result        string // win|loss|draw

	Decisions int
	Legal     int
	Fallbacks int
	// Why a decision was not usable. `Fallbacks` counts substitutions; these say
	// whether the cause was the model's reasoning or the developer's endpoint.
	Illegal         int
	Timeouts        int
	TransportErrors int

	LatencySumMS int64
	LatencyMinMS int64
	LatencyMaxMS int64

	Tokens           int64
	PromptTokens     int64
	CompletionTokens int64
	ReasoningTokens  int64
	CachedTokens     int64
	EstimatedCost    float64

	// Model attribution. Observed is read back from the calls the agent actually made
	// this match (SDK-reported); Declared is the manifest's claim. Either may be empty.
	// The gateway-verified tier is recorded separately by RecordVerifiedCost.
	ObservedProvider string
	ObservedModel    string
	DeclaredProvider string
	DeclaredModel    string
}

// RecordMatchBenchmark upserts one seat's per-match benchmark fact (the P-Index
// Intelligence projection over match.benchmark, and the input the public model
// board aggregates). A no-op when the agent's public id is unknown (INSERT…SELECT
// yields no row) so it never errors on a bot.
func (r *PIndexRepo) RecordMatchBenchmark(ctx context.Context, f MatchBenchmarkFact) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_match_benchmark (
		     match_id, agent_id, game, decisions, legal, fallbacks,
		     illegal, timeouts, transport_errors,
		     latency_sum_ms, latency_min_ms, latency_max_ms,
		     tokens, prompt_tokens, completion_tokens, reasoning_tokens, cached_tokens,
		     estimated_cost, result,
		     observed_provider, observed_model, declared_provider, declared_model, updated_at)
		 SELECT $1, a.id, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17,
		        $18, $19, $20, $21, $22, $23, now()
		 FROM agents a WHERE a.public_id = $2
		 ON CONFLICT (match_id, agent_id) DO UPDATE SET
		   game = EXCLUDED.game, decisions = EXCLUDED.decisions, legal = EXCLUDED.legal,
		   fallbacks = EXCLUDED.fallbacks, illegal = EXCLUDED.illegal, timeouts = EXCLUDED.timeouts,
		   transport_errors = EXCLUDED.transport_errors,
		   latency_sum_ms = EXCLUDED.latency_sum_ms, latency_min_ms = EXCLUDED.latency_min_ms,
		   latency_max_ms = EXCLUDED.latency_max_ms,
		   tokens = EXCLUDED.tokens, prompt_tokens = EXCLUDED.prompt_tokens,
		   completion_tokens = EXCLUDED.completion_tokens, reasoning_tokens = EXCLUDED.reasoning_tokens,
		   cached_tokens = EXCLUDED.cached_tokens,
		   estimated_cost = EXCLUDED.estimated_cost, result = EXCLUDED.result,
		   -- Attribution is only ever UPGRADED by a replay: a re-emitted summary that
		   -- lost its decision log must not blank a model we already resolved.
		   observed_provider = COALESCE(NULLIF(EXCLUDED.observed_provider,''), agent_match_benchmark.observed_provider),
		   observed_model    = COALESCE(NULLIF(EXCLUDED.observed_model,''),    agent_match_benchmark.observed_model),
		   declared_provider = COALESCE(NULLIF(EXCLUDED.declared_provider,''), agent_match_benchmark.declared_provider),
		   declared_model    = COALESCE(NULLIF(EXCLUDED.declared_model,''),    agent_match_benchmark.declared_model),
		   updated_at = now()`,
		f.MatchID, f.AgentPublicID, f.Game, f.Decisions, f.Legal, f.Fallbacks,
		f.Illegal, f.Timeouts, f.TransportErrors,
		f.LatencySumMS, f.LatencyMinMS, f.LatencyMaxMS,
		f.Tokens, f.PromptTokens, f.CompletionTokens, f.ReasoningTokens, f.CachedTokens,
		f.EstimatedCost, f.Result,
		f.ObservedProvider, f.ObservedModel, f.DeclaredProvider, f.DeclaredModel)
	return err
}

// MatchDecision is one recorded move: what the agent did, why, how long it took and
// what it cost. The per-round record behind the per-match aggregate.
type MatchDecision struct {
	Seq       int
	Round     int
	Action    string
	Outcome   string
	LatencyMS int64
	Rationale string

	Provider string
	Model    string
	// Scaffold fingerprints the harness (system prompt, tools, sampling) with the model
	// excluded, so decisions sharing one fingerprint form a controlled comparison.
	// Unstable means it changed mid-turn and the decision cannot be paired.
	Scaffold         string
	ScaffoldUnstable bool
	// ScaffoldIssue is a short code for why there is no fingerprint (e.g.
	// "no_system_prompt"). Empty when one was produced.
	ScaffoldIssue string

	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
	// Cache READS and WRITES, kept apart because they carry different prices in opposite
	// directions and one merged figure cannot be turned back into a cost.
	CachedTokens      int
	CachedWriteTokens int
	TotalTokens       int
	EstimatedCost     float64

	// InputJSON is the turn view the agent was handed, already JSON-encoded and
	// size-capped by the producer. nil when there was none to keep.
	InputJSON []byte
	// InputTruncated marks a view that existed but was dropped for size.
	InputTruncated bool

	// StartedAt is when the engine asked for this move — the timeline anchor. Zero for
	// a record produced before the platform stamped it; stored as NULL, and the client
	// draws no timeline rather than a fabricated one.
	StartedAt time.Time
}

// RecordMatchDecisions persists one seat's decision log for a match.
//
// Written in a single multi-row statement rather than a loop: a 256-move Mafia seat
// would otherwise be 256 round trips inside an event handler that must not become the
// slowest thing in the outbox.
//
// Idempotent on (match_id, agent_id, seq) — the outbox delivers at least once, and a
// redelivered benchmark event must refresh the log rather than duplicate or error on it.
// A no-op when the agent's public id is unknown or the log is empty.
func (r *PIndexRepo) RecordMatchDecisions(ctx context.Context, matchID, agentPublicID string, decisions []MatchDecision) error {
	if matchID == "" || agentPublicID == "" || len(decisions) == 0 {
		return nil
	}
	var agentID int64
	err := r.db.QueryRow(ctx, `SELECT id FROM agents WHERE public_id = $1`, agentPublicID).Scan(&agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // a bot or an unknown seat — nothing to attribute
	}
	if err != nil {
		return err
	}

	rows := make([][]any, 0, len(decisions))
	for _, d := range decisions {
		rows = append(rows, []any{
			matchID, agentID, d.Seq, d.Round, d.Action, d.Outcome, d.LatencyMS, d.Rationale,
			d.Provider, d.Model, d.PromptTokens, d.CompletionTokens, d.ReasoningTokens,
			d.CachedTokens, d.CachedWriteTokens, d.TotalTokens, d.EstimatedCost,
			d.Scaffold, d.ScaffoldUnstable, d.ScaffoldIssue,
			// nil (not "null") so an absent view stores SQL NULL rather than the JSON
			// literal null — the two read back differently and only one is honest.
			inputOrNil(d.InputJSON), d.InputTruncated, timeOrNil(d.StartedAt),
		})
	}

	const cols = 23
	// Position of input_json within a row, named so the ::jsonb cast below cannot drift
	// out of step with the column list the way a bare literal silently would.
	const inputJSONIndex = 20
	args := make([]any, 0, len(rows)*cols)
	var b strings.Builder
	b.WriteString(`INSERT INTO agent_match_decisions (
		match_id, agent_id, seq, round, action, outcome, latency_ms, rationale,
		provider, model, prompt_tokens, completion_tokens, reasoning_tokens,
		cached_tokens, cached_write_tokens, total_tokens, estimated_cost,
		scaffold, scaffold_unstable, scaffold_issue,
		input_json, input_truncated, started_at) VALUES `)
	for i, row := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		for j := range row {
			if j > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(i*cols + j + 1))
			// input_json is column 18 (index 17): pgx sends []byte as bytea unless the
			// placeholder is cast, and a bytea in a jsonb column is a type error at
			// execute time, not at prepare time — so it would only surface in production.
			// This index MUST move whenever a column is added ahead of input_json.
			if j == inputJSONIndex {
				b.WriteString("::jsonb")
			}
		}
		b.WriteByte(')')
		args = append(args, row...)
	}
	b.WriteString(` ON CONFLICT (match_id, agent_id, seq) DO UPDATE SET
		round = EXCLUDED.round, action = EXCLUDED.action, outcome = EXCLUDED.outcome,
		latency_ms = EXCLUDED.latency_ms,
		-- Never blank a rationale we already have: a replayed summary that lost its
		-- decision log must not erase the most useful column in the table.
		rationale = COALESCE(NULLIF(EXCLUDED.rationale,''), agent_match_decisions.rationale),
		provider = COALESCE(NULLIF(EXCLUDED.provider,''), agent_match_decisions.provider),
		model = COALESCE(NULLIF(EXCLUDED.model,''), agent_match_decisions.model),
		prompt_tokens = EXCLUDED.prompt_tokens, completion_tokens = EXCLUDED.completion_tokens,
		reasoning_tokens = EXCLUDED.reasoning_tokens, cached_tokens = EXCLUDED.cached_tokens,
		cached_write_tokens = EXCLUDED.cached_write_tokens,
		-- Never blank a fingerprint we already have: a replayed summary that lost its usage
		-- block must not silently drop an agent out of every paired comparison.
		scaffold = COALESCE(NULLIF(EXCLUDED.scaffold,''), agent_match_decisions.scaffold),
		scaffold_unstable = EXCLUDED.scaffold_unstable OR agent_match_decisions.scaffold_unstable,
		scaffold_issue = EXCLUDED.scaffold_issue,
		total_tokens = EXCLUDED.total_tokens, estimated_cost = EXCLUDED.estimated_cost,
		-- Same rule as the rationale: a replay that lost the view must not erase a view
		-- we already captured.
		input_json = COALESCE(EXCLUDED.input_json, agent_match_decisions.input_json),
		input_truncated = EXCLUDED.input_truncated AND agent_match_decisions.input_json IS NULL,
		-- Same rule again: a replay that lost the timestamp must not erase a real one.
		started_at = COALESCE(EXCLUDED.started_at, agent_match_decisions.started_at)`)

	_, err = r.db.Exec(ctx, b.String(), args...)
	return err
}

// VerifiedCall is one gateway-observed LLM call: the USD cost the server measured,
// the usage the provider itself reported, and the model name the provider returned.
//
// Provider/Model here are the only model attribution on the platform that the agent
// cannot fake — everything else is either the manifest's claim or the SDK's
// self-report. The model benchmark ranks on this tier first.
type VerifiedCall struct {
	MatchID       string // may be empty: a call made outside a match
	AgentPublicID string
	CostUSD       float64
	Provider      string
	Model         string

	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// RecordVerifiedCost accumulates one gateway-observed LLM call into the per-(match,
// agent) verified row (server-measured, unfakeable). A no-op when the agent public id
// is unknown (INSERT…SELECT yields no row). matchID may be empty — such rows still
// aggregate per agent for lifetime cost.
func (r *PIndexRepo) RecordVerifiedCost(ctx context.Context, c VerifiedCall) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_match_verified_cost (
		     match_id, agent_id, verified_cost, calls, provider, model,
		     prompt_tokens, completion_tokens, total_tokens, updated_at)
		 SELECT $1, a.id, $3, 1, $4, $5, $6, $7, $8, now()
		 FROM agents a WHERE a.public_id = $2
		 ON CONFLICT (match_id, agent_id) DO UPDATE SET
		   verified_cost = agent_match_verified_cost.verified_cost + EXCLUDED.verified_cost,
		   calls = agent_match_verified_cost.calls + 1,
		   prompt_tokens     = agent_match_verified_cost.prompt_tokens + EXCLUDED.prompt_tokens,
		   completion_tokens = agent_match_verified_cost.completion_tokens + EXCLUDED.completion_tokens,
		   total_tokens      = agent_match_verified_cost.total_tokens + EXCLUDED.total_tokens,
		   -- Model is LAST-WRITER-WINS among non-empty readings, not accumulated: an
		   -- agent that switched models mid-match is reported as the one it finished on,
		   -- and a call whose response carried no model name never erases a known one.
		   provider = COALESCE(NULLIF(EXCLUDED.provider,''), agent_match_verified_cost.provider),
		   model    = COALESCE(NULLIF(EXCLUDED.model,''),    agent_match_verified_cost.model),
		   updated_at = now()`,
		c.MatchID, c.AgentPublicID, c.CostUSD, c.Provider, c.Model,
		c.PromptTokens, c.CompletionTokens, c.TotalTokens)
	return err
}

// RecordBoundDecision marks one decision as provably LLM-backed: a gateway call
// carrying a proof token the platform minted for exactly this (agent, match, round).
//
// Idempotent on (match, agent, round) BY DESIGN. An agent that makes several calls
// while deciding one move has backed one decision, and must not be able to inflate
// its integrity ratio by retrying or by fanning out across models.
//
// A no-op when the agent public id is unknown (INSERT…SELECT yields no row).
func (r *PIndexRepo) RecordBoundDecision(ctx context.Context, matchID, agentPublicID string, round int) error {
	if matchID == "" {
		return nil // a call outside a match binds to no decision
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_match_bound_decisions (match_id, agent_id, round)
		 SELECT $1, a.id, $3 FROM agents a WHERE a.public_id = $2
		 ON CONFLICT (match_id, agent_id, round) DO NOTHING`,
		matchID, agentPublicID, round)
	return err
}

// BoundDecisions returns how many DISTINCT decisions in this match the agent proved
// were LLM-BACKED AND ACTUALLY MADE. This is the numerator of the ranked integrity ratio,
// so it decides whether a staked match is voided.
//
// # Why it is not simply a row count any more
//
// One completion may now cover a RANGE of rounds — an agent that batches ("plan rounds 4-6
// in one call") gets a bound row per round, which is the whole point: those decisions are
// model-backed and counting calls instead punished the cheapest honest agents at roughly 33%.
//
// But the span is written when the CALL happens, before its later rounds have been played.
// A row for round 6 is therefore a CLAIM about a turn that may never be taken. Counting it
// would let one call at round 1 assert a whole match's worth of coverage, and the ratio that
// voids matches would be reading a promise rather than a decision.
//
// Intersecting with agent_match_decisions costs nothing and makes the count mean what its
// name says. The proof slot is game-dependent for the same reason it is in CoverageFor:
// Goofspiel's `seq` is a submission counter that repeats on a retry, while Mafia's and
// Monopoly's IS the slot the turn proof was minted for.
//
// # And why an unlogged seat still counts everything
//
// This number feeds rule 1 of the ranked gate — "stakes must not flow to a seat that proved
// NOTHING" — which voids a staked match. So a change that can only lower it is not
// automatically safe: if a seat's decisions were never logged, intersecting would take a
// fully-bound seat to zero and VOID the match of an agent that did everything right. The
// decision log has been incomplete before; the pull path wrote no rows at all until
// actdecisions.go, which is how 2067 rated Monopoly matches produced no benchmark facts.
//
// So the intersection applies only to a seat the log actually knows about. No decisions
// logged means no evidence either way, and the count falls back to exactly what it was —
// the same fail-open shape as integrity.Evaluate and movebind.Enforce.
func (r *PIndexRepo) BoundDecisions(ctx context.Context, matchID, agentPublicID string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_match_bound_decisions b
		   JOIN agents a ON a.id = b.agent_id
		   LEFT JOIN matches m ON m.public_id = b.match_id
		  WHERE b.match_id = $1 AND a.public_id = $2
		    AND (
		      NOT EXISTS (
		        SELECT 1 FROM agent_match_decisions d
		         WHERE d.match_id = b.match_id AND d.agent_id = b.agent_id
		      )
		      OR EXISTS (
		        SELECT 1 FROM agent_match_decisions d
		         WHERE d.match_id = b.match_id AND d.agent_id = b.agent_id
		           AND (CASE WHEN m.game = 'goofspiel' THEN d.round ELSE d.seq END) = b.round
		      )
		    )`,
		matchID, agentPublicID).Scan(&n)
	return n, err
}

// TodayStats returns an agent's match count + token spend since the given day
// start (UTC), for the auto-play daily match-cap + token-budget stop-conditions.
func (r *PIndexRepo) TodayStats(ctx context.Context, agentPublicID string, dayStart time.Time) (matches int, tokens int64, err error) {
	err = r.db.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(SUM(amb.tokens), 0)
		   FROM agent_match_benchmark amb JOIN agents a ON a.id = amb.agent_id
		  WHERE a.public_id = $1 AND amb.updated_at >= $2`,
		agentPublicID, dayStart).Scan(&matches, &tokens)
	return matches, tokens, err
}

func (r *PIndexRepo) Save(ctx context.Context, userPublicID string, season int, res pindex.Result, inputsHash string, asOf time.Time) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var uid int64
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE public_id = $1`, userPublicID).Scan(&uid); err != nil {
		return err
	}

	var prev float64
	err = tx.QueryRow(ctx, `SELECT p_index FROM developer_pindex WHERE user_id = $1 AND season = $2`, uid, season).Scan(&prev)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	delta := res.PIndex - prev

	if _, err := tx.Exec(ctx,
		`INSERT INTO developer_pindex
		   (user_id, season, p_index, arena_c, consistency_c, difficulty_c, activity_c, intelligence_c,
		    skill_c, highest_pindex, best_rank, config_version, computed_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$11,$3,0,$9,$10)
		 ON CONFLICT (user_id, season) DO UPDATE SET
		   p_index = EXCLUDED.p_index, arena_c = EXCLUDED.arena_c,
		   consistency_c = EXCLUDED.consistency_c, difficulty_c = EXCLUDED.difficulty_c,
		   activity_c = EXCLUDED.activity_c, intelligence_c = EXCLUDED.intelligence_c,
		   skill_c = EXCLUDED.skill_c,
		   highest_pindex = GREATEST(developer_pindex.highest_pindex, EXCLUDED.p_index),
		   config_version = EXCLUDED.config_version, computed_at = EXCLUDED.computed_at`,
		uid, season, res.PIndex, res.Sub("arena"), res.Sub("consistency"),
		res.Sub("difficulty"), res.Sub("activity"), res.Sub("intelligence"), res.ConfigVersion, asOf,
		res.Sub("skill")); err != nil {
		return err
	}

	breakdown, err := json.Marshal(res.Contributions)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO developer_pindex_history
		   (user_id, season, p_index, breakdown, delta, config_version, inputs_hash, computed_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		uid, season, res.PIndex, breakdown, delta, res.ConfigVersion, inputsHash, asOf); err != nil {
		return err
	}

	// Emit pindex.updated in the same tx (transactional outbox): rank-threshold
	// badges + analytics project off it.
	payload, err := json.Marshal(map[string]any{
		"developer": userPublicID, "season": season, "p_index": res.PIndex,
		"delta": delta, "contributions": res.Contributions,
	})
	if err != nil {
		return err
	}
	if _, err := InsertEventTx(ctx, tx, events.TypePIndexUpdated, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PIndexRepo) EnqueueDirtyByAgents(ctx context.Context, agentPublicIDs []string) error {
	if len(agentPublicIDs) == 0 {
		return nil
	}
	// DO UPDATE (not DO NOTHING): re-enqueueing an already-dirty developer BUMPS
	// enqueued_at, changing the claim token so a recompute in flight can't clear it
	// and drop this update (see ClearDirty).
	_, err := r.db.Exec(ctx,
		`INSERT INTO pindex_dirty (user_id)
		 SELECT DISTINCT a.owner_user_id FROM agents a WHERE a.public_id = ANY($1)
		 ON CONFLICT (user_id) DO UPDATE SET enqueued_at = now()`, agentPublicIDs)
	return err
}

func (r *PIndexRepo) EnqueueDirty(ctx context.Context, userPublicIDs []string) error {
	if len(userPublicIDs) == 0 {
		return nil
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO pindex_dirty (user_id)
		 SELECT id FROM users WHERE public_id = ANY($1)
		 ON CONFLICT (user_id) DO UPDATE SET enqueued_at = now()`, userPublicIDs)
	return err
}

func (r *PIndexRepo) PeekDirty(ctx context.Context, limit int) ([]pindex.Dirty, error) {
	rows, err := r.db.Query(ctx,
		`SELECT u.public_id, d.enqueued_at FROM pindex_dirty d JOIN users u ON u.id = d.user_id
		 ORDER BY d.enqueued_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pindex.Dirty
	for rows.Next() {
		var d pindex.Dirty
		if err := rows.Scan(&d.UserPublicID, &d.Token); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *PIndexRepo) ClearDirty(ctx context.Context, userPublicID string, token time.Time) (bool, error) {
	ct, err := r.db.Exec(ctx,
		`DELETE FROM pindex_dirty
		 WHERE user_id = (SELECT id FROM users WHERE public_id = $1) AND enqueued_at = $2`,
		userPublicID, token)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}

// Rank recomputes global_rank and percentile over the PUBLISHED population — the same
// population DevProfileRepo.Leaderboard shows, via the same publishedDeveloper predicate.
//
// Ranking everyone and publishing only some is the bug this shape exists to avoid, and the
// published ladder learned it first (see SnapshotRanks): a rank computed over one population
// and displayed on a board drawn from another is a number that cannot be found. A developer
// would read "rank 7" on their own profile, open the board, and be absent from it — while the
// six above them silently included agents the arena will not vouch for.
//
// Two statements, deliberately, because an unpublished developer needs their rank CLEARED and
// not merely left out of the UPDATE. Leaving it out is what makes a stale rank outlive the
// verification that earned it: a developer who once had a bound call keeps rank 7 forever if
// the row is only ever written and never reset. 0 is already the wire value for "unranked"
// (see openapi.yaml global_rank, and the directory's COALESCE), so this says the true thing
// in the vocabulary the API already has.
//
// p_index itself is NOT touched by either statement. It is computed for everyone, exactly as
// ratings are, and an unpublished developer keeps theirs — only its rank is withheld.
//
// best_rank likewise only ever moves on the published side. It is a lifetime high-water mark,
// so an unpublished developer keeps the best rank they held while published rather than having
// it zeroed; nothing about losing publication makes a past position untrue.
func (r *PIndexRepo) Rank(ctx context.Context, season int) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// One transaction so the board is never read between the two writes, when a developer
	// could otherwise be ranked by neither statement or counted by both.
	if _, err := tx.Exec(ctx,
		`WITH ranked AS (
		   SELECT d.user_id,
		          ROW_NUMBER() OVER (ORDER BY d.p_index DESC, d.user_id) AS rnk,
		          COUNT(*)     OVER ()                                   AS total
		   FROM developer_pindex d JOIN users u ON u.id = d.user_id
		   WHERE d.season = $1 AND `+publishedDeveloper("u")+`
		 )
		 UPDATE developer_pindex d SET
		   global_rank = r.rnk,
		   percentile  = CASE WHEN r.total > 0 THEN ROUND(100.0 * r.rnk / r.total, 2) ELSE 0 END,
		   best_rank   = CASE WHEN d.best_rank = 0 THEN r.rnk ELSE LEAST(d.best_rank, r.rnk) END
		 FROM ranked r WHERE d.user_id = r.user_id AND d.season = $1`, season); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE developer_pindex d SET global_rank = 0, percentile = 0
		 FROM users u
		 WHERE u.id = d.user_id AND d.season = $1
		   AND NOT `+publishedDeveloper("u")+`
		   AND (d.global_rank <> 0 OR d.percentile <> 0)`, season); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *PIndexRepo) Get(ctx context.Context, userPublicID string, season int) (pindex.Snapshot, bool, error) {
	s := pindex.Snapshot{UserPublicID: userPublicID, Season: season}
	err := r.db.QueryRow(ctx,
		`SELECT p_index, arena_c, consistency_c, difficulty_c, activity_c,
		        global_rank, percentile, highest_pindex, best_rank, config_version, computed_at
		 FROM developer_pindex
		 WHERE user_id = (SELECT id FROM users WHERE public_id = $1) AND season = $2`,
		userPublicID, season).
		Scan(&s.PIndex, &s.Arena, &s.Consistency, &s.Difficulty, &s.Activity,
			&s.GlobalRank, &s.Percentile, &s.HighestPIndex, &s.BestRank, &s.ConfigVersion, &s.ComputedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return pindex.Snapshot{}, false, nil
	}
	if err != nil {
		return pindex.Snapshot{}, false, err
	}
	return s, true, nil
}

func (r *PIndexRepo) History(ctx context.Context, userPublicID string, limit int) ([]pindex.HistoryEntry, error) {
	rows, err := r.db.Query(ctx,
		`SELECT season, p_index, delta, breakdown, config_version, computed_at
		 FROM developer_pindex_history
		 WHERE user_id = (SELECT id FROM users WHERE public_id = $1)
		 ORDER BY computed_at DESC LIMIT $2`, userPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pindex.HistoryEntry
	for rows.Next() {
		var h pindex.HistoryEntry
		var breakdown []byte
		if err := rows.Scan(&h.Season, &h.PIndex, &h.Delta, &breakdown, &h.ConfigVersion, &h.ComputedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(breakdown, &h.Breakdown); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// inputOrNil converts an empty capture to a true SQL NULL.
//
// Passing an empty []byte would store the four bytes "null" as a jsonb value, which
// reads back as a present-but-null view — indistinguishable from an agent that really
// was handed nothing. The distinction is the whole point of input_truncated.
func inputOrNil(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// timeOrNil converts a zero time to SQL NULL.
//
// Storing year 1 would put every pre-stamp decision at the start of a timeline that
// spans two millennia, which is a more confident lie than storing nothing.
func timeOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// ── Admin config surface ─────────────────────────────────────────────────────

// ListConfigs returns every scoring config, newest first.
func (r *PIndexRepo) ListConfigs(ctx context.Context) ([]pindex.VersionedConfig, error) {
	rows, err := r.db.Query(ctx,
		`SELECT version, active, params FROM pindex_config ORDER BY version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pindex.VersionedConfig
	for rows.Next() {
		var c pindex.VersionedConfig
		if err := rows.Scan(&c.Version, &c.Active, &c.Params); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutConfig writes a version's params, leaving its active flag untouched.
//
// Never activates. Writing a candidate must not change what developers are being scored
// on — a P-Index change re-ranks everyone at once, so that has to be a separate,
// deliberate act. Same separation as writing a doc version versus publishing it.
func (r *PIndexRepo) PutConfig(ctx context.Context, version int, params []byte) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO pindex_config (version, params, active) VALUES ($1, $2, false)
		 ON CONFLICT (version) DO UPDATE SET params = EXCLUDED.params`,
		version, params)
	return err
}

// ActivateConfig makes exactly one version live.
//
// Clear-then-set inside one transaction. The obvious one-liner
// `UPDATE pindex_config SET active = (version = $1)` LOOKS atomic, but the UNIQUE
// partial index on active is enforced PER ROW as the statement walks the table: if
// the target row is updated before the currently-active row is cleared, two rows are
// briefly active at once and Postgres rejects the whole statement. Whether that
// happens depends on physical row order, so it fails exactly when activating a version
// whose row precedes the live one — i.e. rolling BACK to an older version reliably 500s.
//
// Deactivating first (0 active) then activating (1 active) can never produce two active
// rows regardless of order. The interim 0-active state lives only inside this
// transaction: MVCC keeps concurrent recomputes reading the previous active row until
// COMMIT flips them to the new one, so no reader ever sees zero active configs.
func (r *PIndexRepo) ActivateConfig(ctx context.Context, version int) error {
	var exists bool
	if err := r.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pindex_config WHERE version = $1)`, version).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return httpx.NewError(404, "not_found", "no such P-Index config version")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE pindex_config SET active = false WHERE active`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE pindex_config SET active = true WHERE version = $1`, version); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
