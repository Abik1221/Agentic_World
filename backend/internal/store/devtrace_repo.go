package store

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/devtrace"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DevTraceRepo backs the trace read path from Postgres: which agents a user owns, and
// what those agents actually did.
//
// It used to answer only the first question, because /traces was a thin window onto a
// separate telemetry service. That made a first-class page as available as an external
// dependency, and the page spent most of its life saying the trace store was unreachable.
// The arena's own match log has the answers — see internal/devtrace/local.go.
type DevTraceRepo struct{ db *pgxpool.Pool }

func NewDevTraceRepo(db *pgxpool.Pool) *DevTraceRepo { return &DevTraceRepo{db: db} }

var (
	_ devtrace.Repo      = (*DevTraceRepo)(nil)
	_ devtrace.LocalRepo = (*DevTraceRepo)(nil)
	_ devtrace.MatchRepo = (*DevTraceRepo)(nil)
)

// OwnedAgentIDs returns the public ids of the user's own agents.
//
// House agents are excluded: they are platform-run opponents, not the developer's
// work, and their reasoning is exactly the kind of thing a developer should not be
// able to read. A user with no agents gets an empty slice, which the caller turns
// into an empty result rather than an unfiltered query.
func (r *DevTraceRepo) OwnedAgentIDs(ctx context.Context, userPublicID string) ([]string, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id FROM agents a
		 WHERE a.owner_user_id = (SELECT id FROM users WHERE public_id = $1)
		   AND a.kind = 'external'
		 ORDER BY a.created_at`, userPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, 4)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// matchActivityQuery joins the match log to the caller's own seats.
//
// ATTRIBUTION is the whole difficulty. A match_events row belongs to a MATCH, not to an
// agent; match_players is what says which seat is whose. So this produces one row per
// (event, caller's agent in that match) and the service picks out the parts of each payload
// belonging to that seat.
//
// Scope, spelled out because this query is the thing that must not leak:
//   - `a.public_id = ANY($1)` is restricted to the agents the ownership gate already
//     resolved, so a row can only ever be attributed to an agent the caller owns.
//   - Event types are allow-listed here as well as in the mapper. `card_sealed` carries NO
//     card value by design (an opponent must not learn a move before both seats commit),
//     and `round_revealed` is public the instant it is emitted — it is what spectators and
//     the replay see. Nothing hidden about the opponent is selected.
//   - Ordered by event id, not created_at: several events in one round share a timestamp to
//     the millisecond, so id is what makes a bound stable.
//
// THE ALLOWLIST covers all three games. It used to name only Goofspiel's kinds
// ('prize_revealed','card_sealed','round_revealed') plus the two shared ones, so a Mafia
// match — which emits phase|moderator|message|vote|eliminate|victory — matched NOTHING and
// was invisible on the traces page, and a Monopoly match rendered as two bare lines. A
// developer with a hundred Mafia matches saw a page that looked broken.
//
// `night` IS DELIBERATELY ABSENT and must stay absent. Mafia's night payload carries the
// mafia's chosen target and a `secret` field; during a LIVE match that is the one fact a
// surviving player must not be able to read, and the cheapest place to guarantee that is
// here, where no mapper bug can reach it.
const traceEventTypes = `(
  'match_created','match_finished','agent_says',
  'prize_revealed','card_sealed','round_revealed',
  'phase','moderator','message','vote','eliminate','victory',
  'turn_started','dice_rolled','moved','cash_changed','rent_paid','property_purchased',
  'card_drawn','went_to_jail','left_jail','house_built','house_sold','mortgaged','unmortgaged',
  'auction_started','bid_placed','auction_passed','auction_won','auction_unsold','bankrupt',
  'trade_proposed','trade_executed','trade_rejected','turn_ended'
)`

const matchActivityQuery = `
SELECT m.public_id, m.game, a.public_id, mp.seat, me.type, me.payload, me.created_at
  FROM match_events me
  JOIN matches       m  ON m.id  = me.match_id
  JOIN match_players mp ON mp.match_id = m.id
  JOIN agents        a  ON a.id  = mp.agent_id
 WHERE a.public_id = ANY($1)
   AND me.created_at >= $2
   AND me.type IN ` + traceEventTypes + `
 ORDER BY me.id DESC
 LIMIT $3`

func (r *DevTraceRepo) MatchActivity(
	ctx context.Context, agentPublicIDs []string, since time.Time, limit int,
) ([]devtrace.MatchRow, error) {
	if len(agentPublicIDs) == 0 {
		return nil, nil
	}
	// Fetched generously relative to the caller's limit: several raw rows collapse into one
	// timeline entry (a round's prize reveal, the seal and the reveal become a single
	// decision line), so bounding the SQL at the entry count would truncate the newest
	// round mid-way and report it with no latency.
	fetch := limit * 6
	if fetch <= 0 || fetch > 6000 {
		fetch = 6000
	}
	rows, err := r.db.Query(ctx, matchActivityQuery, agentPublicIDs, since.UTC(), fetch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]devtrace.MatchRow, 0, 256)
	for rows.Next() {
		var m devtrace.MatchRow
		if err := rows.Scan(&m.MatchPublicID, &m.Game, &m.AgentPublicID, &m.Seat,
			&m.Type, &m.Payload, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AgentRegistrations answers "when did this agent come into existence", so a developer who
// has not played yet is shown something true rather than an empty timeline that reads as a
// broken page.
func (r *DevTraceRepo) AgentRegistrations(
	ctx context.Context, agentPublicIDs []string,
) ([]devtrace.Registration, error) {
	if len(agentPublicIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.Query(ctx,
		`SELECT public_id, name, created_at FROM agents
		  WHERE public_id = ANY($1) ORDER BY created_at DESC`, agentPublicIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []devtrace.Registration
	for rows.Next() {
		var reg devtrace.Registration
		if err := rows.Scan(&reg.AgentPublicID, &reg.Name, &reg.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, reg)
	}
	return out, rows.Err()
}

// ── Game history (paginated) ─────────────────────────────────────────────────
//
// One row per (match, the caller's agent in it). The joins are worth reading carefully
// because three of them are LEFT joins onto tables keyed by the match's PUBLIC id as
// TEXT, not by matches.id:
//
//	agent_match_benchmark      (match_id TEXT, agent_id BIGINT) — decisions/legal/fallbacks/
//	                           latency/tokens/estimated_cost/result, written by the drive loop
//	agent_match_verified_cost  (match_id TEXT, agent_id BIGINT) — gateway-observed USD + calls
//	agent_match_bound_decisions(match_id TEXT, agent_id BIGINT, round) — proof-carrying turns
//
// They are LEFT joins on purpose: a match with no telemetry row is a real and common
// state (a deterministic agent making no LLM calls), and it must read as zeroes rather
// than vanishing from the developer's own history. Reporting "you have never played"
// because a metering row is missing would be the worse error by far.
//
// `players` is a correlated count rather than a join so the aggregate cannot multiply
// the metering rows — with a twelve-seat Mafia match a naive join would report twelve
// times the tokens.
const matchHistorySelect = `
SELECT m.public_id, m.game, m.mode, m.status,
       a.public_id, a.name, mp.seat,
       (SELECT COUNT(*) FROM match_players mp2 WHERE mp2.match_id = m.id),
       m.started_at, m.finished_at,
       COALESCE(b.result, ''), mp.final_score,
       m.bid, mp.coins_delta,
       COALESCE(b.decisions, 0), COALESCE(b.legal, 0), COALESCE(b.fallbacks, 0),
       CASE WHEN COALESCE(b.decisions, 0) > 0
            THEN COALESCE(b.latency_sum_ms, 0) / b.decisions ELSE 0 END,
       COALESCE(b.tokens, 0), COALESCE(b.estimated_cost, 0),
       COALESCE(v.verified_cost, 0), COALESCE(v.calls, 0),
       COALESCE((SELECT COUNT(*) FROM agent_match_bound_decisions d
                  WHERE d.match_id = m.public_id AND d.agent_id = a.id), 0)
  FROM match_players mp
  JOIN matches m ON m.id = mp.match_id
  JOIN agents  a ON a.id = mp.agent_id
  LEFT JOIN agent_match_benchmark     b ON b.match_id = m.public_id AND b.agent_id = a.id
  LEFT JOIN agent_match_verified_cost v ON v.match_id = m.public_id AND v.agent_id = a.id
 WHERE a.public_id = ANY($1)`

// Matches implements devtrace.MatchRepo.
//
// The total is counted with the SAME predicate as the page, in one round trip per
// question. It counts MATCHES, which is what the client paginates over — a total taken
// from a different predicate (or from an event count) is how the old page came to say
// "100+ games" above a list that could only ever reach four of them.
func (r *DevTraceRepo) Matches(
	ctx context.Context, agentPublicIDs []string, mode devtrace.MatchMode, limit, offset int,
) ([]devtrace.MatchSummary, int, error) {
	if len(agentPublicIDs) == 0 {
		return nil, 0, nil
	}
	// $2 carries the mode; the empty string means "any". Done as a predicate rather than
	// by concatenating SQL so the filter is always a bound parameter.
	const modeClause = ` AND ($2 = '' OR m.mode = $2)`

	var total int
	if err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM match_players mp
		   JOIN matches m ON m.id = mp.match_id
		   JOIN agents  a ON a.id = mp.agent_id
		  WHERE a.public_id = ANY($1)`+modeClause,
		agentPublicIDs, string(mode)).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}

	// Ordered by start, then by match id as a tiebreak: several matches can be created
	// in the same millisecond, and without a stable second key a page boundary can drop
	// or repeat a row between requests. COALESCE so a match that never started (still
	// waiting) sorts by when it was created rather than sinking below everything.
	rows, err := r.db.Query(ctx,
		matchHistorySelect+modeClause+`
		 ORDER BY COALESCE(m.started_at, m.created_at) DESC, m.public_id DESC
		 LIMIT $3 OFFSET $4`,
		agentPublicIDs, string(mode), limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]devtrace.MatchSummary, 0, limit)
	for rows.Next() {
		s, err := scanMatchSummary(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}

// MatchSummaryFor implements devtrace.MatchRepo. Restricted to the caller's own agents,
// so a match they had no seat in resolves to ErrNoMatch rather than to a 403 that would
// confirm the match exists.
func (r *DevTraceRepo) MatchSummaryFor(
	ctx context.Context, agentPublicIDs []string, matchPublicID string,
) (devtrace.MatchSummary, error) {
	if len(agentPublicIDs) == 0 || matchPublicID == "" {
		return devtrace.MatchSummary{}, devtrace.ErrNoMatch
	}
	rows, err := r.db.Query(ctx, matchHistorySelect+` AND m.public_id = $2 LIMIT 1`,
		agentPublicIDs, matchPublicID)
	if err != nil {
		return devtrace.MatchSummary{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return devtrace.MatchSummary{}, err
		}
		return devtrace.MatchSummary{}, devtrace.ErrNoMatch
	}
	return scanMatchSummary(rows)
}

// scanMatchSummary reads one matchHistorySelect row. Nullable columns land in pointers
// so the API can distinguish "zero" from "not recorded" — a final score of 0 and no
// score at all are different facts, and a UI that renders both as "0" is lying about
// one of them.
func scanMatchSummary(rows pgx.Rows) (devtrace.MatchSummary, error) {
	var s devtrace.MatchSummary
	var started, finished *time.Time
	var score *int
	var coins *int64
	if err := rows.Scan(
		&s.MatchID, &s.Game, &s.Mode, &s.Status,
		&s.AgentID, &s.AgentName, &s.Seat, &s.Players,
		&started, &finished,
		&s.Result, &score,
		&s.Stake, &coins,
		&s.Decisions, &s.Legal, &s.Fallbacks, &s.AvgLatencyMS,
		&s.Tokens, &s.SelfReported, &s.VerifiedCost, &s.VerifiedCalls, &s.BoundDecisions,
	); err != nil {
		return devtrace.MatchSummary{}, err
	}
	s.StartedAt, s.FinishedAt, s.Score, s.CoinsDelta = started, finished, score, coins
	return s, nil
}

// Roster implements devtrace.MatchRepo: every seat in the match.
//
// Opponents' NAMES and results are shown — they are already public on the spectator
// view and the replay, and a Mafia timeline is unreadable without them. Their decisions
// and reasoning are not here and never come from this query; see games.go for the rule
// on which of their events are admitted, and when.
func (r *DevTraceRepo) Roster(
	ctx context.Context, matchPublicID string, ownedAgentIDs []string,
) ([]devtrace.RosterSeat, error) {
	rows, err := r.db.Query(ctx,
		`SELECT mp.seat, a.public_id, a.name, a.kind = 'house', mp.final_score, mp.coins_delta
		   FROM match_players mp
		   JOIN matches m ON m.id = mp.match_id
		   JOIN agents  a ON a.id = mp.agent_id
		  WHERE m.public_id = $1
		  ORDER BY mp.seat`, matchPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	mine := make(map[string]bool, len(ownedAgentIDs))
	for _, id := range ownedAgentIDs {
		mine[id] = true
	}
	out := make([]devtrace.RosterSeat, 0, 12)
	for rows.Next() {
		var s devtrace.RosterSeat
		var score *int
		var coins *int64
		if err := rows.Scan(&s.Seat, &s.AgentID, &s.AgentName, &s.House, &score, &coins); err != nil {
			return nil, err
		}
		s.Mine = mine[s.AgentID]
		s.FinalScore = score
		// Another developer's coin movement is their business — it is the stake they
		// chose and what they lost, which is exactly the kind of thing the rest of this
		// package refuses to show. Only the caller's own row carries it.
		if s.Mine {
			s.CoinsDelta = coins
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MatchEvents implements devtrace.MatchRepo: the allowlisted log for ONE match,
// attributed to the caller's seat. Ordered ASCENDING by event id and then truncated
// from the FRONT if it overflows: a match's opening is what explains its ending, so
// when something has to be dropped it is the middle, not the start.
func (r *DevTraceRepo) MatchEvents(
	ctx context.Context, agentPublicIDs []string, matchPublicID string, limit int,
) ([]devtrace.MatchRow, error) {
	if len(agentPublicIDs) == 0 || matchPublicID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 8000 {
		limit = 4000
	}
	rows, err := r.db.Query(ctx,
		`SELECT m.public_id, m.game, a.public_id, mp.seat, me.type, me.payload, me.created_at
		   FROM match_events me
		   JOIN matches       m  ON m.id = me.match_id
		   JOIN match_players mp ON mp.match_id = m.id
		   JOIN agents        a  ON a.id = mp.agent_id
		  WHERE a.public_id = ANY($1)
		    AND m.public_id = $2
		    AND me.type IN `+traceEventTypes+`
		  ORDER BY me.id ASC
		  LIMIT $3`,
		agentPublicIDs, matchPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]devtrace.MatchRow, 0, 256)
	for rows.Next() {
		var m devtrace.MatchRow
		if err := rows.Scan(&m.MatchPublicID, &m.Game, &m.AgentPublicID, &m.Seat,
			&m.Type, &m.Payload, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MatchDecisions returns the caller's own per-move record for one match, in order.
//
// SCOPED AT THE QUERY, not filtered after. A rationale is the agent's private reasoning
// about its opponents, so the agent-id list is part of the WHERE clause — there is no
// code path that reads another seat's decisions and then discards them, because the
// path that discards them is the one that eventually forgets to.
func (r *DevTraceRepo) MatchDecisions(
	ctx context.Context, agentPublicIDs []string, matchPublicID string, limit int,
) ([]devtrace.Decision, error) {
	if len(agentPublicIDs) == 0 || matchPublicID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 4000 {
		limit = 1000
	}
	rows, err := r.db.Query(ctx,
		`SELECT d.seq, d.round, d.action, d.outcome, d.latency_ms, d.rationale,
		        d.provider, d.model, d.prompt_tokens, d.completion_tokens,
		        d.reasoning_tokens, d.cached_tokens, d.cached_write_tokens,
		        d.total_tokens, d.estimated_cost, d.scaffold, d.scaffold_unstable, d.scaffold_issue,
		        d.input_json, d.input_truncated, d.started_at,
		        d.skill_regret, d.skill_best
		   FROM agent_match_decisions d
		   JOIN agents a ON a.id = d.agent_id
		  WHERE d.match_id = $1 AND a.public_id = ANY($2)
		  ORDER BY d.seq
		  LIMIT $3`, matchPublicID, agentPublicIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []devtrace.Decision
	for rows.Next() {
		var d devtrace.Decision
		var input []byte
		// skill_best is NULL on unscored rows; skill_regret stays a pointer so "not
		// scored" survives all the way to the client rather than arriving as a zero.
		var best *string
		if err := rows.Scan(&d.Seq, &d.Round, &d.Action, &d.Outcome, &d.LatencyMS, &d.Rationale,
			&d.Provider, &d.Model, &d.PromptTokens, &d.CompletionTokens,
			&d.ReasoningTokens, &d.CachedTokens, &d.CachedWriteTokens,
			&d.TotalTokens, &d.EstimatedCost, &d.Scaffold, &d.ScaffoldUnstable, &d.ScaffoldIssue,
			&input, &d.InputTruncated, &d.StartedAt,
			&d.SkillRegret, &best); err != nil {
			return nil, err
		}
		d.Input = input
		if best != nil {
			d.SkillBest = *best
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ── cross-match agent telemetry ──────────────────────────────────────────────
//
// SCOPING NOTE, because this is where aggregates leak. Each query below applies
// `a.public_id = ANY($1)` inside the SAME statement that groups — not in a wrapper, not
// afterwards in Go. An aggregate that forgets its filter returns a summary of every agent
// on the platform and looks entirely plausible while doing it, which is the one bug class
// a reviewer cannot spot from the numbers.

const agentTelemetryBaseCTE = `
WITH d AS (
  SELECT dd.match_id, dd.seq, dd.round, dd.action, dd.outcome, dd.latency_ms, dd.rationale,
         dd.total_tokens, dd.estimated_cost, dd.started_at,
         COALESCE(m.game, '') AS game
  FROM agent_match_decisions dd
  JOIN agents a  ON a.id = dd.agent_id AND a.public_id = ANY($1)
  LEFT JOIN matches m ON m.public_id = dd.match_id
  WHERE dd.created_at >= $2
)`

// AgentTelemetry rolls up the per-decision record for the caller's agents over a window.
func (r *DevTraceRepo) AgentTelemetry(ctx context.Context, agentPublicIDs []string, since time.Time) (devtrace.AgentTelemetry, error) {
	var out devtrace.AgentTelemetry
	if len(agentPublicIDs) == 0 {
		return out, nil
	}

	// Headline totals. percentile_disc (not a mean) because the tail is the thing that
	// times out under load, and a mean hides it.
	err := r.db.QueryRow(ctx, agentTelemetryBaseCTE+`
		SELECT COUNT(DISTINCT match_id)::int,
		       COUNT(*)::int,
		       COUNT(*) FILTER (WHERE outcome <> 'ok' AND outcome <> '')::int,
		       COALESCE(AVG((outcome = 'ok')::int), 0)::double precision,
		       COALESCE(percentile_disc(0.50) WITHIN GROUP (ORDER BY latency_ms), 0)::int,
		       COALESCE(percentile_disc(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)::int,
		       COALESCE(percentile_disc(0.99) WITHIN GROUP (ORDER BY latency_ms), 0)::int,
		       COALESCE(MAX(latency_ms), 0)::int,
		       COALESCE(SUM(total_tokens), 0)::bigint,
		       COALESCE(SUM(estimated_cost), 0)::double precision,
		       COALESCE(AVG((rationale <> '')::int), 0)::double precision
		FROM d`, agentPublicIDs, since).
		Scan(&out.Matches, &out.Decisions, &out.Failures, &out.LegalRate,
			&out.P50Ms, &out.P95Ms, &out.P99Ms, &out.MaxMs,
			&out.Tokens, &out.CostUSD, &out.ReasonedShare)
	if err != nil {
		return devtrace.AgentTelemetry{}, err
	}

	// Failures by cause, worst first — "what breaks" before "how often anything breaks".
	rows, err := r.db.Query(ctx, agentTelemetryBaseCTE+`
		SELECT outcome, COUNT(*)::int
		FROM d WHERE outcome <> 'ok' AND outcome <> ''
		GROUP BY outcome ORDER BY 2 DESC, 1`, agentPublicIDs, since)
	if err != nil {
		return devtrace.AgentTelemetry{}, err
	}
	for rows.Next() {
		var f devtrace.FailureCount
		if err := rows.Scan(&f.Outcome, &f.Count); err != nil {
			rows.Close()
			return devtrace.AgentTelemetry{}, err
		}
		out.FailuresByCause = append(out.FailuresByCause, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return devtrace.AgentTelemetry{}, err
	}

	// Per arena. Failure modes are arena-specific, so this is never averaged away.
	rows, err = r.db.Query(ctx, agentTelemetryBaseCTE+`
		SELECT game, COUNT(DISTINCT match_id)::int, COUNT(*)::int,
		       COUNT(*) FILTER (WHERE outcome <> 'ok' AND outcome <> '')::int,
		       COALESCE(AVG((outcome = 'ok')::int), 0)::double precision,
		       COALESCE(percentile_disc(0.50) WITHIN GROUP (ORDER BY latency_ms), 0)::int,
		       COALESCE(percentile_disc(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)::int,
		       COALESCE(SUM(total_tokens), 0)::bigint,
		       COALESCE(SUM(estimated_cost), 0)::double precision
		FROM d WHERE game <> ''
		GROUP BY game ORDER BY 3 DESC`, agentPublicIDs, since)
	if err != nil {
		return devtrace.AgentTelemetry{}, err
	}
	for rows.Next() {
		var a devtrace.ArenaTelemetry
		if err := rows.Scan(&a.Game, &a.Matches, &a.Decisions, &a.Failures,
			&a.LegalRate, &a.P50Ms, &a.P95Ms, &a.Tokens, &a.CostUSD); err != nil {
			rows.Close()
			return devtrace.AgentTelemetry{}, err
		}
		out.Arenas = append(out.Arenas, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return devtrace.AgentTelemetry{}, err
	}

	// Hotspots: rounds that fail disproportionately.
	//
	// The minimum sample is the whole design of this list. Too low and a single failure
	// out of one decision is a 100% failure rate that tops the list forever; too high and
	// a new agent — which is exactly who needs this page — sees nothing. Three is the
	// lowest floor that excludes the pure-noise 1-of-1 and 2-of-2 cases while surfacing a
	// real pattern within a couple of matches.
	rows, err = r.db.Query(ctx, agentTelemetryBaseCTE+`
		SELECT round, game, COUNT(*)::int,
		       COUNT(*) FILTER (WHERE outcome <> 'ok' AND outcome <> '')::int
		FROM d WHERE game <> ''
		GROUP BY round, game
		HAVING COUNT(*) >= 3 AND COUNT(*) FILTER (WHERE outcome <> 'ok' AND outcome <> '') > 0
		ORDER BY (COUNT(*) FILTER (WHERE outcome <> 'ok' AND outcome <> ''))::float / COUNT(*) DESC,
		         COUNT(*) DESC
		LIMIT 8`, agentPublicIDs, since)
	if err != nil {
		return devtrace.AgentTelemetry{}, err
	}
	for rows.Next() {
		var h devtrace.RoundHotspot
		if err := rows.Scan(&h.Round, &h.Game, &h.Decisions, &h.Failures); err != nil {
			rows.Close()
			return devtrace.AgentTelemetry{}, err
		}
		if h.Decisions > 0 {
			h.Rate = float64(h.Failures) / float64(h.Decisions)
		}
		out.Hotspots = append(out.Hotspots, h)
	}
	rows.Close()
	return out, rows.Err()
}

// AgentWorstDecisions returns the slowest decisions and the most recent failures — the
// rows a developer should open first.
func (r *DevTraceRepo) AgentWorstDecisions(
	ctx context.Context, agentPublicIDs []string, since time.Time, limit int,
) (slowest, failures []devtrace.WorstDecision, err error) {
	if len(agentPublicIDs) == 0 {
		return nil, nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	scan := func(q string) ([]devtrace.WorstDecision, error) {
		rows, err := r.db.Query(ctx, agentTelemetryBaseCTE+q, agentPublicIDs, since, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []devtrace.WorstDecision
		for rows.Next() {
			var w devtrace.WorstDecision
			if err := rows.Scan(&w.MatchID, &w.Seq, &w.Game, &w.Round, &w.Outcome,
				&w.LatencyMS, &w.Action, &w.Rationale, &w.At); err != nil {
				return nil, err
			}
			out = append(out, w)
		}
		return out, rows.Err()
	}

	const cols = `
		SELECT match_id, seq, game, round, outcome, latency_ms, action, rationale, started_at
		FROM d`
	if slowest, err = scan(cols + ` ORDER BY latency_ms DESC LIMIT $3`); err != nil {
		return nil, nil, err
	}
	// Recent, not "worst": the failures that matter are the ones still happening. Ordered
	// by the decision's own clock where there is one, falling back to write order.
	failures, err = scan(cols + `
		WHERE outcome <> 'ok' AND outcome <> ''
		ORDER BY COALESCE(started_at, TIMESTAMPTZ '-infinity') DESC, seq DESC
		LIMIT $3`)
	if err != nil {
		return nil, nil, err
	}
	return slowest, failures, nil
}
