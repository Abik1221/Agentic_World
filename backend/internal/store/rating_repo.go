package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/events"
	"github.com/agent-arena/arena/internal/rating"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RatingRepo is the pgx implementation of rating.Repo. ApplyMatch is the
// transactional read-modify-write: it writes the idempotency marker first, locks
// both agents' rating rows in a deadlock-free order (ascending id), and applies
// the ELO change the rating package computes via the Compute closure.
type RatingRepo struct{ db *pgxpool.Pool }

func NewRatingRepo(db *pgxpool.Pool) *RatingRepo { return &RatingRepo{db: db} }

var _ rating.Repo = (*RatingRepo)(nil)

// publishedAgent is the "staked but unranked" rule, as SQL.
//
// An agent that never routes a model call may play staked tables and win coins. It is simply
// not published on a ranked surface, because the arena cannot say a model chose its moves. The
// incentive to verify is reputational, not financial.
//
// # Computed for everyone, published for the verified
//
// This filters the PUBLICATION, never the computation. Ratings keep updating for every agent,
// because Glicko/Elo quality depends on a connected comparison graph: 55 of the 75 rated
// non-house agents here are unverified, and dropping four fifths of the population from the
// rating maths would degrade the VERIFIED agents' numbers too — the same separability concern
// the model board already tracks. So every match still moves both seats' ratings; only the
// board's SELECT is narrowed.
//
// # Why this predicate and not a coverage threshold
//
// It matches the model board's `no_verified_model` exclusion exactly — a call the gateway
// PROVED belonged to a decision, which named a model — so "ranked" means one fact rather than
// two that can drift apart. Measured against the live database, the two candidate definitions
// (a bound model call vs. any bound decision) selected the identical 20 agents, so the weaker
// one buys nothing.
//
// Deliberately "has EVER proven one", not "proves some percentage". How MUCH of an agent's play
// is verified is the ranked-integrity threshold's question and it is measured per match; this is
// the prior question of whether the agent routes at all.
//
// alias is the agents-table alias in the calling query.
// The status guard is here for the same reason it is on the model board's `verified` CTE, and
// it has to be on BOTH or the two definitions drift — which the paragraph above promises they
// do not. `bound` is set from the turn proof before the upstream is called, so on its own it
// admits a call the provider refused: 108 harness calls to openrouter.ai in the lab were 429s
// and 401s with zero tokens and bound=true. "Routes at all" has to mean a provider answered,
// not that we successfully sent something and were turned away.
func publishedAgent(alias string) string {
	return `EXISTS (SELECT 1 FROM agent_model_calls mc
	                 WHERE mc.agent_id = ` + alias + `.id AND mc.bound AND mc.status BETWEEN 200 AND 299 AND COALESCE(mc.model,'') <> ''
	                   AND mc.status BETWEEN 200 AND 299)`
}

// publishedDeveloper is the same rule one level up: a developer is published once ANY of
// their real agents is.
//
// The developer board and the model board are described as one dataset seen two ways, and
// only one of them enforced it. The model board ranks proven decisions; the developer board
// ranked whoever had a P-Index row, so a developer could hold a public rank built on
// self-reported attribution while the model board refused to name their model at all. The
// two surfaces disagreed about what "ranked" means, and the weaker one was the one with a
// number on it.
//
// Defined in terms of publishedAgent rather than beside it. A second EXISTS spelling of
// "verified" would be a second definition — it would pass review, and then drift the first
// time the model-call schema changes and only one of the two is updated.
//
// ANY agent, not ALL: a developer who verified one agent and is still wiring up a second is
// published, exactly as an agent that proved one call is. "How much of their play is
// verified" is the ranked-integrity threshold's question, measured per match.
//
// House agents are excluded here, matching every other developer-facing query — the house
// never stakes and its owner is not a competitor.
//
// alias is the users-table alias in the calling query.
func publishedDeveloper(alias string) string {
	return `EXISTS (SELECT 1 FROM agents a_pub
	                 WHERE a_pub.owner_user_id = ` + alias + `.id AND a_pub.kind = 'external'
	                   AND ` + publishedAgent("a_pub") + `)`
}

func (r *RatingRepo) ApplyMatch(ctx context.Context, in rating.ApplyInput) (bool, error) {
	if len(in.Players) < 2 {
		return false, nil
	}
	game := in.Game
	if game == "" {
		game = rating.GameGoofspiel
	}
	algo := in.Algo
	if algo == "" {
		algo = rating.AlgoGlicko2
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Idempotency marker: present ⇒ already rated. Bound to a real match via FK. A
	// match belongs to exactly one arena, so match_id alone is the right key.
	var matchID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO rating_updates (match_id, season)
		 SELECT m.id, $2 FROM matches m WHERE m.public_id = $1
		 ON CONFLICT (match_id) DO NOTHING
		 RETURNING match_id`, in.MatchPublicID, in.Season).Scan(&matchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, tx.Commit(ctx) // already rated (or no such match): no-op
	}
	if err != nil {
		return false, err
	}

	// Resolve agent ids first (player order preserved so Compute's output aligns).
	ids := make([]int64, len(in.Players))
	for i, p := range in.Players {
		id, err := resolveAgentID(ctx, tx, p.AgentPublicID)
		if err != nil {
			return false, err
		}
		ids[i] = id
	}
	// Ensure a rating row exists for each (agent, game, season), inserting in
	// ASCENDING agent-id order — the same order as the FOR UPDATE lock below — so two
	// concurrent matches that share agents can't deadlock on the ensure-insert.
	order := make([]int, len(ids))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return ids[order[a]] < ids[order[b]] })
	for _, i := range order {
		if _, err := tx.Exec(ctx,
			`INSERT INTO ratings (agent_id, game, season, algo) VALUES ($1, $2, $3, $4)
			 ON CONFLICT DO NOTHING`, ids[i], game, in.Season, algo); err != nil {
			return false, err
		}
	}

	// Lock all rows in ascending id order (deadlock-free across concurrent matches
	// that share an agent).
	locked := append([]int64(nil), ids...)
	sort.Slice(locked, func(i, j int) bool { return locked[i] < locked[j] })
	cur := map[int64]rating.RatingState{}
	streak := map[int64]int{}
	rows, err := tx.Query(ctx,
		`SELECT agent_id, elo, rd, vol, mu, sigma, current_streak FROM ratings
		 WHERE agent_id = ANY($1) AND game = $2 AND season = $3
		 ORDER BY agent_id FOR UPDATE`, locked, game, in.Season)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var id, s int64
		var e int
		var rd, vol, mu, sigma float64
		if err := rows.Scan(&id, &e, &rd, &vol, &mu, &sigma, &s); err != nil {
			rows.Close()
			return false, err
		}
		cur[id] = rating.RatingState{Elo: e, RD: rd, Vol: vol, Mu: mu, Sigma: sigma}
		streak[id] = int(s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	// Build the aligned inputs, compute, and write each player's new state + a
	// permanent per-match snapshot row.
	states := make([]rating.RatingState, len(in.Players))
	placements := make([]int, len(in.Players))
	minPlace := in.Players[0].Placement
	allEqual := true
	for i, p := range in.Players {
		states[i] = cur[ids[i]]
		placements[i] = p.Placement
		if p.Placement < minPlace {
			minPlace = p.Placement
		}
		if p.Placement != in.Players[0].Placement {
			allEqual = false
		}
	}
	next := in.Compute(states, placements)
	if len(next) != len(in.Players) {
		return false, errors.New("rating: Compute returned wrong player count")
	}

	deltas := make([]ratingDelta, len(in.Players))
	for i, p := range in.Players {
		var w, l, t int
		switch {
		case allEqual:
			t = 1
		case p.Placement == minPlace:
			w = 1
		default:
			l = 1
		}
		if err := updateRating(ctx, tx, ids[i], game, in.Season, next[i], streak[ids[i]], w, l, t, p.CoinsDelta); err != nil {
			return false, err
		}
		before, after := states[i].Elo, next[i].Elo
		if _, err := tx.Exec(ctx,
			`INSERT INTO match_rating_changes
			   (match_id, agent_id, game, season, rating_before, rating_after, rating_delta, rank_in_match)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			 ON CONFLICT (match_id, agent_id) DO NOTHING`,
			matchID, ids[i], game, in.Season, before, after, after-before, p.Placement); err != nil {
			return false, err
		}
		deltas[i] = ratingDelta{
			Agent: p.AgentPublicID, Before: before, After: after,
			Delta: after - before, Rank: p.Placement,
		}
	}

	// Emit rating.updated in the SAME tx (transactional outbox): the P-Index
	// recompute pipeline and streak/win-count badges project off this.
	payload, err := json.Marshal(map[string]any{
		"match": in.MatchPublicID, "game": game, "season": in.Season, "agents": deltas,
	})
	if err != nil {
		return false, err
	}
	if _, err := InsertEventTx(ctx, tx, events.TypeRatingUpdated, payload); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// ratingDelta is one agent's rating movement in the rating.updated event payload.
type ratingDelta struct {
	Agent  string `json:"agent"`
	Before int    `json:"rating_before"`
	After  int    `json:"rating_after"`
	Delta  int    `json:"rating_delta"`
	Rank   int    `json:"rank"`
}

func (r *RatingRepo) Leaderboard(ctx context.Context, game string, season, offset, limit int) ([]rating.LeaderRow, error) {
	// The window RANK() runs over the full (game,season) set before LIMIT/OFFSET, so
	// cur.rnk is the true global rank; trend = the most recent prior day's rank minus
	// today's (positive = the agent climbed). Agents with no prior snapshot read 0.
	rows, err := r.db.Query(ctx,
		`SELECT cur.public_id, cur.slug, cur.name, cur.avatar_url, cur.elo, cur.rd,
		        cur.wins, cur.losses, cur.ties, cur.coins_earned, cur.current_streak,
		        CASE WHEN prev.rank IS NULL THEN 0 ELSE prev.rank - cur.rnk END AS trend
		 FROM (
		   SELECT a.public_id, a.slug, a.name, COALESCE(a.avatar_url, '') AS avatar_url,
		          r.elo, COALESCE(r.rd, 0) AS rd, r.wins, r.losses, r.ties,
		          r.coins_earned, r.current_streak, r.agent_id, r.game, r.season,
		          RANK() OVER (ORDER BY r.elo DESC, r.agent_id ASC) AS rnk
		   FROM ratings r JOIN agents a ON a.id = r.agent_id
		   WHERE r.game = $1 AND r.season = $2 AND a.kind = 'external'
		     AND `+publishedAgent("a")+`
		 ) cur
		 LEFT JOIN LATERAL (
		   SELECT s.rank FROM rating_rank_snapshots s
		   WHERE s.game = cur.game AND s.season = cur.season
		     AND s.agent_id = cur.agent_id AND s.taken_on < CURRENT_DATE
		   ORDER BY s.taken_on DESC LIMIT 1
		 ) prev ON true
		 ORDER BY cur.rnk
		 LIMIT $3 OFFSET $4`, game, season, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rating.LeaderRow
	for rows.Next() {
		var lr rating.LeaderRow
		if err := rows.Scan(&lr.AgentPublicID, &lr.Slug, &lr.Name, &lr.AvatarURL, &lr.Elo, &lr.RD,
			&lr.Wins, &lr.Losses, &lr.Ties, &lr.CoinsEarned, &lr.Streak, &lr.Trend); err != nil {
			return nil, err
		}
		out = append(out, lr)
	}
	return out, rows.Err()
}

// SnapshotRanks records the current rank of every PUBLISHED agent per (game,
// season) for the given day. Idempotent per day via the UNIQUE(taken_on) key, so
// running it more than once a day (or on multiple instances) is safe.
//
// Filtered by the same publishedAgent rule as the board itself, and that is not optional.
// These snapshots are what the board's `trend` column subtracts from today's rank, so a
// snapshot taken over a different population than the board displays would report movement
// nobody made: with 55 unverified agents interleaved in yesterday's ranks and absent from
// today's, every published agent would appear to have climbed.
func (r *RatingRepo) SnapshotRanks(ctx context.Context, takenOn time.Time) (int, error) {
	tag, err := r.db.Exec(ctx,
		`INSERT INTO rating_rank_snapshots (game, season, agent_id, rank, taken_on)
		 SELECT r.game, r.season, r.agent_id,
		        RANK() OVER (PARTITION BY r.game, r.season ORDER BY r.elo DESC, r.agent_id ASC),
		        $1::date
		 FROM ratings r JOIN agents a ON a.id = r.agent_id
		 WHERE a.kind = 'external' AND `+publishedAgent("a")+`
		 ON CONFLICT (game, season, agent_id, taken_on) DO NOTHING`, takenOn)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// modelBenchmarkSQL aggregates a season's per-match benchmark facts by the model that
// actually played each match.
//
// $1 season · $2 game (” = every arena) · $3 window start · $4 window end
//
// Shape: one row per (provider, model) TOTAL plus one row per (provider, model, game),
// produced in a single pass with GROUPING SETS rather than by two round trips over the
// same facts. is_total=1 marks the aggregate row.
//
// Why the attribution ladder (verified → observed → declared) rather than the manifest
// alone: this query used to start from agent_manifests, so a model appeared only if its
// developer had hand-written a `model:` block. `pyyol init` does not scaffold one, which
// meant the board's real result set was EMPTY while the gateway sat on the provider's
// own model name for every call. Resolution happens per match, so a developer who
// switches models mid-season has each match credited to the model that played it.
// modelFactCTE resolves every benchmarked match in the window to the model that
// actually played it. Shared VERBATIM by the board aggregate and the per-model detail
// query: if the two resolved attribution even slightly differently, a model's detail
// page would disagree with the row the reader clicked to reach it.
//
// $1 game (” = every arena) · $2 window start · $3 window end
//
// The CTE owns the LOW parameter numbers and callers append their own from $4 up. It
// used to reserve $1 for the season, which only the aggregate's ratings join uses — so
// the developer query, which needs no season, inherited a parameter it never referenced
// and Postgres could not determine its type ("could not determine data type of
// parameter $1"). A shared fragment must not leave a hole for its callers to fill.
const modelFactCTE = `
WITH decl AS (
  -- Latest non-rejected manifest declaration per agent — the weakest tier, and only
  -- consulted when neither the gateway nor the SDK identified the model.
  SELECT DISTINCT ON (agent_public_id) agent_public_id,
         COALESCE(model_provider,'') AS provider, COALESCE(model_name,'') AS model
  FROM agent_manifests
  WHERE status <> 'rejected'
    AND COALESCE(model_provider,'') <> '' AND COALESCE(model_name,'') <> ''
  ORDER BY agent_public_id, created_at DESC
),
fact AS (
  SELECT b.agent_id, b.game, b.updated_at, b.result,
         COALESCE(NULLIF(v.model,''),    NULLIF(b.observed_model,''),
                  NULLIF(b.declared_model,''),    NULLIF(dc.model,''),    '') AS model,
         COALESCE(NULLIF(v.provider,''), NULLIF(b.observed_provider,''),
                  NULLIF(b.declared_provider,''), NULLIF(dc.provider,''), '') AS provider,
         CASE WHEN NULLIF(v.model,'')          IS NOT NULL THEN 1   -- gateway-verified
              WHEN NULLIF(b.observed_model,'') IS NOT NULL THEN 2   -- SDK-observed
              ELSE 3 END                                          AS attr_rank,
         b.decisions, b.legal, b.fallbacks, b.illegal, b.timeouts, b.transport_errors,
         b.latency_sum_ms, b.latency_min_ms, b.latency_max_ms,
         b.tokens, b.prompt_tokens, b.completion_tokens, b.reasoning_tokens, b.cached_tokens,
         b.estimated_cost,
         COALESCE(v.verified_cost,0) AS verified_cost,
         COALESCE(v.calls,0)         AS verified_calls,
         -- Verified COVERAGE for this seat: distinct decisions proven LLM-backed by a turn
         -- proof, over decisions actually logged.
         --
         -- The tier used to be MIN(attr_rank) alone, so ONE verified call out of thousands of
         -- decisions labelled a whole model row "verified". That is exploitable in the
         -- direction that rewards doing less: cost per win is computed from VERIFIED cost, so
         -- an agent routing 1% of its calls reports 1% of its spend against 100% of its wins
         -- and tops a cost-efficiency board precisely BECAUSE it declined to be measured.
         -- Carrying the denominator is what makes the numerator safe to publish.
         --
         -- DISTINCT decisions, not calls: an agent may make forty calls for one decision (a
         -- best-of-N sample, a tool loop), and counting calls would let volume manufacture
         -- coverage.
         COALESCE(bd.bound_decisions,0) AS bound_decisions,
         COALESCE(dl.logged_decisions,0) AS logged_decisions,
         -- Real match wall-clock. NULL (not 0) when the clock is unusable, so a stuck
         -- or aborted match drops out of the DURATION average without also discarding
         -- the tokens it genuinely burned.
         CASE WHEN m.started_at IS NOT NULL AND m.finished_at > m.started_at
              THEN EXTRACT(EPOCH FROM (m.finished_at - m.started_at)) END AS match_seconds
  FROM agent_match_benchmark b
  JOIN agents  a ON a.id = b.agent_id AND a.kind = 'external'
  -- Only FINISHED matches inside the season window count. The window is applied here,
  -- on the fact's own match, so an agent's totals cannot leak across a season boundary.
  JOIN matches m ON m.public_id = b.match_id
                AND m.finished_at IS NOT NULL
                AND m.finished_at >= $2 AND m.finished_at < $3
                -- Exclude tables that house bots had to fill to reach their roster
                -- (matches.rated = false, migration 0076). The a.kind = 'external' join
                -- above only drops the BOTS' own rows; the human seats at such a table
                -- are real agents making real LLM calls, so without this the board would
                -- credit a model for beating engine bots.
                AND m.rated
  LEFT JOIN agent_match_verified_cost v ON v.match_id = b.match_id AND v.agent_id = b.agent_id
  LEFT JOIN (
    SELECT match_id, agent_id, COUNT(DISTINCT round)::bigint AS bound_decisions
      FROM agent_match_bound_decisions GROUP BY match_id, agent_id
  ) bd ON bd.match_id = b.match_id AND bd.agent_id = b.agent_id
  LEFT JOIN (
    SELECT match_id, agent_id, COUNT(*)::bigint AS logged_decisions
      FROM agent_match_decisions GROUP BY match_id, agent_id
  ) dl ON dl.match_id = b.match_id AND dl.agent_id = b.agent_id
  LEFT JOIN decl dc ON dc.agent_public_id = a.public_id
  WHERE ($1 = '' OR b.game = $1)
)`

// modelBenchmarkSQL aggregates the resolved facts by model.
//
// $1 game · $2 window start · $3 window end · $4 season
//
// Shape: one row per (provider, model) TOTAL plus one row per (provider, model, game),
// produced in a single pass with GROUPING SETS rather than by two round trips over the
// same facts. is_total=1 marks the aggregate row.
// modelFactCTEHarness is modelFactCTE with ONE change: which seats are eligible.
//
// Derived by substitution rather than copied, deliberately. Every other line — the token
// and latency aggregation, the cost join, the verification coverage, the attribution
// tiering — is the SAME source, so the harness stats mean exactly what the developer stats
// mean and cannot drift from them in a later edit. A hand-copied second query would be
// identical for about one release.
//
// Two conditions differ, and both have to:
//
//	kind: 'harness' instead of 'external'. Obvious.
//	rated: dropped. The developer query uses m.rated to exclude tables house bots filled,
//	  because crediting a model for beating an engine bot is fiction. Harness matches are
//	  UNRATED BY DESIGN — that is what keeps them out of user ratings — so keeping the
//	  filter would return nothing at all. The equivalent guard is enforced on the harness
//	  board's own seat query: every seat at the table must itself be a harness agent.
var modelFactCTEHarness = func() string {
	s := strings.Replace(modelFactCTE,
		"JOIN agents  a ON a.id = b.agent_id AND a.kind = 'external'",
		"JOIN agents  a ON a.id = b.agent_id AND a.kind = 'harness'", 1)
	if s == modelFactCTE {
		panic("rating: harness fact CTE substitution missed the kind join — the source moved")
	}
	out := strings.Replace(s, "\n                AND m.rated\n", "\n", 1)
	if out == s {
		panic("rating: harness fact CTE substitution missed the rated filter — the source moved")
	}
	return out
}()

// modelBenchmarkHarnessSQL serves the platform harness board's per-model operational stats:
// decisions, thinking time, reasoning tokens and cost.
var modelBenchmarkHarnessSQL = modelFactCTEHarness + strings.TrimPrefix(modelBenchmarkSQL, modelFactCTE)

const modelBenchmarkSQL = modelFactCTE + `,
agg AS (
  SELECT provider, model, game, GROUPING(game) AS is_total,
         MIN(attr_rank)::int                               AS attr_rank,
         COUNT(*)::int                                     AS matches,
         COUNT(*) FILTER (WHERE result = 'win')::int       AS wins,
         COUNT(*) FILTER (WHERE result = 'loss')::int      AS losses,
         COUNT(*) FILTER (WHERE result = 'draw')::int      AS ties,
         COALESCE(SUM(decisions),0)::bigint                AS decisions,
         COALESCE(SUM(legal),0)::bigint                    AS legal,
         COALESCE(SUM(fallbacks),0)::bigint                AS fallbacks,
         COALESCE(SUM(illegal),0)::bigint                  AS illegal,
         COALESCE(SUM(timeouts),0)::bigint                 AS timeouts,
         COALESCE(SUM(transport_errors),0)::bigint         AS transport_errors,
         COALESCE(SUM(latency_sum_ms),0)::bigint           AS latency_sum_ms,
         -- 0 means "not measured" in these columns, so it must not win a MIN().
         COALESCE(MIN(NULLIF(latency_min_ms,0)),0)::bigint AS latency_min_ms,
         COALESCE(MAX(latency_max_ms),0)::bigint           AS latency_max_ms,
         COALESCE(SUM(tokens),0)::bigint                   AS tokens,
         COALESCE(SUM(prompt_tokens),0)::bigint            AS prompt_tokens,
         COALESCE(SUM(completion_tokens),0)::bigint        AS completion_tokens,
         COALESCE(SUM(reasoning_tokens),0)::bigint         AS reasoning_tokens,
         COALESCE(SUM(cached_tokens),0)::bigint            AS cached_tokens,
         COALESCE(SUM(estimated_cost),0)::double precision AS est_cost,
         COALESCE(SUM(verified_cost),0)::double precision  AS verified_cost,
         COALESCE(SUM(verified_calls),0)::bigint           AS verified_calls,
         COALESCE(SUM(bound_decisions),0)::bigint          AS bound_decisions,
         COALESCE(SUM(logged_decisions),0)::bigint         AS logged_decisions,
         COALESCE(SUM(match_seconds),0)::double precision  AS play_seconds,
         COUNT(match_seconds)::int                         AS timed_matches
  FROM fact
  WHERE model <> ''
  GROUP BY GROUPING SETS ((provider, model), (provider, model, game))
),
am AS (
  -- The model each agent MOST RECENTLY played in an arena. ELO and coins live on
  -- ratings, which is keyed by (agent, game, season) and has no per-match grain, so
  -- they can only be attributed to one model per agent per arena — the current one.
  SELECT DISTINCT ON (agent_id, game) agent_id, game, provider, model
  FROM fact WHERE model <> ''
  ORDER BY agent_id, game, updated_at DESC
),
elo AS (
  SELECT am.provider, am.model, am.game, GROUPING(am.game) AS is_total,
         COUNT(DISTINCT am.agent_id)::int         AS agents,
         -- How many distinct DEVELOPERS chose this model. Adoption is a different
         -- signal from agent count: one person running twelve agents is not twelve
         -- people betting on the model, and only the second is evidence of anything.
         COUNT(DISTINCT ag.owner_user_id)::int    AS developers,
         COALESCE(ROUND(AVG(r.elo)),0)::int       AS avg_elo,
         COALESCE(SUM(r.coins_earned),0)::bigint  AS coins_won
  FROM am
  JOIN agents ag ON ag.id = am.agent_id
  JOIN ratings r ON r.agent_id = am.agent_id AND r.game = am.game AND r.season = $4
  GROUP BY GROUPING SETS ((am.provider, am.model), (am.provider, am.model, am.game))
)
SELECT a.provider, a.model, COALESCE(a.game,''), a.is_total, a.attr_rank,
       COALESCE(e.agents,0), COALESCE(e.developers,0), COALESCE(e.avg_elo,0), COALESCE(e.coins_won,0),
       a.matches, a.wins, a.losses, a.ties,
       a.decisions, a.legal, a.fallbacks, a.illegal, a.timeouts, a.transport_errors,
       a.latency_sum_ms, a.latency_min_ms, a.latency_max_ms,
       a.tokens, a.prompt_tokens, a.completion_tokens, a.reasoning_tokens, a.cached_tokens,
       a.est_cost, a.verified_cost, a.verified_calls,
       a.bound_decisions, a.logged_decisions,
       a.play_seconds, a.timed_matches
FROM agg a
LEFT JOIN elo e ON e.provider = a.provider AND e.model = a.model
                AND e.is_total = a.is_total
                AND (a.is_total = 1 OR e.game = a.game)
-- Total row first for each model, then its arenas: lets the scan below attach arena
-- rows to the aggregate it just built without a second pass or a lookup map.
ORDER BY a.provider, a.model, a.is_total DESC, a.game`

// ModelBenchmark aggregates a season's benchmark facts per model, for one arena or
// (game == "") every arena with a per-arena breakdown attached. See modelBenchmarkSQL.
func (r *RatingRepo) ModelBenchmark(ctx context.Context, season int, game string, start, end time.Time) ([]rating.ModelStat, error) {
	return r.modelStats(ctx, modelBenchmarkSQL, season, game, start, end)
}

// HarnessModelBenchmark is the same aggregation over the PLATFORM's own benchmark matches.
//
// Same columns, same arithmetic, same meaning — a latency or a cost here is directly
// comparable with one on the developer board, which is the entire reason the two share a
// query body.
func (r *RatingRepo) HarnessModelBenchmark(ctx context.Context, season int, game string, start, end time.Time) ([]rating.ModelStat, error) {
	return r.modelStats(ctx, modelBenchmarkHarnessSQL, season, game, start, end)
}

func (r *RatingRepo) modelStats(ctx context.Context, sql string, season int, game string, start, end time.Time) ([]rating.ModelStat, error) {
	rows, err := r.db.Query(ctx, sql, game, start, end, season)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []rating.ModelStat
	for rows.Next() {
		var (
			provider, model, rowGame string
			isTotal                  int
			s                        rating.ModelStat
			latSum                   int64
			latMin, latMax           int64
			// Verified coverage counts. Scanned separately because the row publishes the
			// FRACTION, and building it in one place keeps the clamp and the
			// unknown-vs-zero distinction out of the SQL.
			boundDecisions, loggedDecisions int64
		)
		if err := rows.Scan(&provider, &model, &rowGame, &isTotal, &s.AttrRank,
			&s.Agents, &s.Developers, &s.AvgElo, &s.CoinsWon,
			&s.Matches, &s.Wins, &s.Losses, &s.Ties,
			&s.Decisions, &s.Legal, &s.Fallbacks, &s.Illegal, &s.Timeouts, &s.TransportErrors,
			&latSum, &latMin, &latMax,
			&s.Tokens, &s.PromptTokens, &s.CompletionTokens, &s.ReasoningTokens, &s.CachedTokens,
			&s.EstCostUSD, &s.VerifiedCostUSD, &s.VerifiedCalls,
			&boundDecisions, &loggedDecisions,
			&s.PlaySeconds, &s.TimedMatches); err != nil {
			return nil, err
		}
		s.Provider, s.Model = provider, model
		// Coverage first, then the tier FROM coverage: an identification path can only ever
		// be downgraded by how little of the row it actually covers, never upgraded.
		s.Verified = rating.NewCoverage(int(loggedDecisions), int(boundDecisions))
		s.MinLatencyMs, s.MaxLatencyMs = int(latMin), int(latMax)
		if s.Decisions > 0 {
			s.AvgLatencyMs = int(float64(latSum)/float64(s.Decisions) + 0.5)
		}

		if isTotal == 1 {
			out = append(out, s)
			continue
		}
		// An arena row. Its total row precedes it (see ORDER BY), so it belongs to the
		// last model appended. If it somehow does not, drop it rather than mis-file it.
		if n := len(out); n > 0 && out[n-1].Provider == provider && out[n-1].Model == model {
			out[n-1].Arenas = append(out[n-1].Arenas, rating.ArenaStat{
				Game: rowGame, Agents: s.Agents, Developers: s.Developers, Matches: s.Matches,
				Wins: s.Wins, Losses: s.Losses, Ties: s.Ties,
				AvgElo: s.AvgElo, CoinsWon: s.CoinsWon,
				Decisions: s.Decisions, Tokens: s.Tokens, AvgLatencyMs: s.AvgLatencyMs,
				EstCostUSD: s.EstCostUSD, VerifiedCostUSD: s.VerifiedCostUSD,
				// Derived fields (rates, per-match figures) are filled by the service,
				// which owns every derivation on this board so the two grains cannot
				// drift apart.
				PlaySecondsInternal: s.PlaySeconds, TimedMatchesInternal: s.TimedMatches,
				LegalInternal: s.Legal,
			})
		}
	}
	return out, rows.Err()
}

// modelRunnersSQL lists the agents that actually played a given model this season, with
// their owner, so the model's detail page can answer "who is running this, and how are
// they doing with it".
//
// $1 game · $2 window start · $3 window end · $4 season · $5 provider · $6 model
//
// Built on the SAME resolved facts as the board, so an agent appears here under exactly
// the model the board credited its matches to. Everything selected is already public on
// the leaderboard and developer profiles — this is a different arrangement of it, not a
// new disclosure.
const modelRunnersSQL = modelFactCTE + `
SELECT a.public_id, a.name, a.slug, COALESCE(a.avatar_url,''),
       COALESCE(u.username::text,''), COALESCE(u.display_name,''), COALESCE(u.avatar_url,''),
       COUNT(*)::int                                     AS matches,
       COUNT(*) FILTER (WHERE f.result = 'win')::int     AS wins,
       COUNT(*) FILTER (WHERE f.result = 'loss')::int    AS losses,
       COUNT(*) FILTER (WHERE f.result = 'draw')::int    AS ties,
       COALESCE(SUM(f.tokens),0)::bigint                 AS tokens,
       COALESCE(SUM(f.decisions),0)::bigint              AS decisions,
       COALESCE(SUM(f.estimated_cost),0)::double precision AS est_cost,
       COALESCE(SUM(f.verified_cost),0)::double precision  AS verified_cost,
       -- The agent's BEST rating among the arenas it played this model in. An agent
       -- that runs one model in Mafia and another in Monopoly must not have its
       -- Monopoly rating attributed to the Mafia model, which a blanket join would do.
       COALESCE(MAX(r.elo),0)::int                       AS elo
FROM fact f
JOIN agents a ON a.id = f.agent_id
JOIN users  u ON u.id = a.owner_user_id
LEFT JOIN ratings r ON r.agent_id = f.agent_id AND r.game = f.game AND r.season = $4
WHERE f.model = $6 AND f.provider = $5
GROUP BY a.public_id, a.name, a.slug, a.avatar_url, u.username, u.display_name, u.avatar_url
ORDER BY matches DESC, wins DESC, a.public_id
LIMIT 100`

// ModelRunners returns the agents that played this model in the window, best-adopted
// first. Capped at 100 rows: this is a "who runs this" panel, not a second leaderboard.
func (r *RatingRepo) ModelRunners(ctx context.Context, season int, game, provider, model string, start, end time.Time) ([]rating.ModelRunner, error) {
	rows, err := r.db.Query(ctx, modelRunnersSQL, game, start, end, season, provider, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rating.ModelRunner
	for rows.Next() {
		var m rating.ModelRunner
		if err := rows.Scan(&m.AgentPublicID, &m.AgentName, &m.AgentSlug, &m.AgentAvatarURL,
			&m.Username, &m.DisplayName, &m.DeveloperAvatarURL,
			&m.Matches, &m.Wins, &m.Losses, &m.Ties,
			&m.Tokens, &m.Decisions, &m.EstCostUSD, &m.VerifiedCostUSD, &m.Elo); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AgentStanding returns an agent's rank + totals for the season. Rank is 1-based,
// ordered by ELO desc (ties broken by lower agent_id, matching the leaderboard).
//
// game may be "" for "the agent's PRIMARY arena" — the one it has played most this
// season, ELO breaking ties. The caller gets the resolved arena back in Standing.Game,
// which it must show: a rank is only meaningful next to the arena it is a rank in.
func (r *RatingRepo) AgentStanding(ctx context.Context, season int, game, agentPublicID string) (rating.Standing, bool, error) {
	var s rating.Standing
	var attrRank int
	var boundDecisions, loggedDecisions int64
	s.Season = season
	s.AgentPublicID = agentPublicID
	err := r.db.QueryRow(ctx,
		`SELECT r.game, a.name, r.elo, r.wins, r.losses, r.ties, r.coins_earned, r.current_streak,
		   -- Rank is computed WITHIN r.game rather than within the requested arena, so
		   -- it stays correct when the arena was resolved here instead of passed in.
		   -- Rank and total count the PUBLISHED population (see publishedAgent), so this
		   -- number means the same thing as the one on the board. Counting unverified agents
		   -- here would tell a developer they are 40th of 75 while the ladder they are
		   -- comparing against has 20 rows.
		   (SELECT COUNT(*)+1 FROM ratings r2 JOIN agents a2 ON a2.id = r2.agent_id
		      WHERE r2.game = r.game AND r2.season = $1 AND a2.kind = 'external'
		        AND `+publishedAgent("a2")+`
		        AND (r2.elo > r.elo OR (r2.elo = r.elo AND r2.agent_id < r.agent_id))) AS rank,
		   (SELECT COUNT(*) FROM ratings r3 JOIN agents a3 ON a3.id = r3.agent_id
		      WHERE r3.game = r.game AND r3.season = $1 AND a3.kind = 'external'
		        AND `+publishedAgent("a3")+`) AS total,
		   -- Whether this agent is itself on the board. An unverified agent still has a real
		   -- rating and real coins; it is simply not published, and saying so plainly is the
		   -- whole point of the policy — the alternative is a developer seeing a rank on their
		   -- own page and not finding themselves on the ladder.
		   `+publishedAgent("a")+` AS ranked,
		   -- agent_manifests is keyed by agent_public_id; it has no agent_id column.
		   -- Referencing one made this whole query fail with "column m.agent_id does not
		   -- exist", so /v1/rankings/standing answered 500 for every agent and the
		   -- console's "your rank this season" card silently never rendered.
		   -- Provider/model resolved by TRUST, best source first, mirroring the model board:
		   -- what the gateway saw the provider return, then what the SDK reported per call,
		   -- then the manifest. This used to read the manifest alone and present it untagged,
		   -- so a model the developer merely typed looked exactly like one we had confirmed.
		   COALESCE(gw.provider, NULLIF(bm.observed_provider,''),
		            (SELECT model_provider FROM agent_manifests m WHERE m.agent_public_id = a.public_id
		               AND m.status <> 'rejected' ORDER BY created_at DESC LIMIT 1), ''),
		   COALESCE(gw.model, NULLIF(bm.observed_model,''),
		            (SELECT model_name FROM agent_manifests m WHERE m.agent_public_id = a.public_id
		               AND m.status <> 'rejected' ORDER BY created_at DESC LIMIT 1), ''),
		   -- Which of the three answered (1=gateway, 2=SDK, 3=manifest). Coverage downgrades
		   -- it afterwards; it can never promote it.
		   CASE WHEN gw.model IS NOT NULL THEN 1
		        WHEN NULLIF(bm.observed_model,'') IS NOT NULL THEN 2 ELSE 3 END,
		   COALESCE(cov.bound_decisions,0), COALESCE(cov.logged_decisions,0)
		 FROM ratings r JOIN agents a ON a.id = r.agent_id
		 -- The most recent model the GATEWAY observed on a bound call. Bound only: an unbound
		 -- call proves nothing about which model decided a move, and this is the top tier.
		 LEFT JOIN LATERAL (
		   SELECT mc.provider, mc.model FROM agent_model_calls mc
		    WHERE mc.agent_id = a.id AND mc.bound AND mc.status BETWEEN 200 AND 299 AND mc.model <> ''
		    ORDER BY mc.id DESC LIMIT 1
		 ) gw ON true
		 -- The most recent model the SDK reported for a real match.
		 LEFT JOIN LATERAL (
		   SELECT b.observed_provider, b.observed_model FROM agent_match_benchmark b
		    WHERE b.agent_id = a.id AND COALESCE(b.observed_model,'') <> ''
		    ORDER BY b.updated_at DESC LIMIT 1
		 ) bm ON true
		 -- Coverage over this agent's whole logged history, so the tier reflects how much of
		 -- its play was proven rather than that any single call was.
		 LEFT JOIN LATERAL (
		   SELECT (SELECT COUNT(DISTINCT (bd.match_id, bd.round)) FROM agent_match_bound_decisions bd
		            WHERE bd.agent_id = a.id) AS bound_decisions,
		          (SELECT COUNT(*) FROM agent_match_decisions dd
		            WHERE dd.agent_id = a.id) AS logged_decisions
		 ) cov ON true
		 WHERE r.season = $1 AND a.public_id = $2 AND ($3 = '' OR r.game = $3)
		 -- With no arena requested, the agent's most-played arena wins. Most-played
		 -- rather than highest-ELO on purpose: showing someone their best rating from an
		 -- arena they tried twice would be a flattering number, not their standing.
		 ORDER BY (r.wins + r.losses + r.ties) DESC, r.elo DESC
		 LIMIT 1`,
		season, agentPublicID, game).
		Scan(&s.Game, &s.Name, &s.Elo, &s.Wins, &s.Losses, &s.Ties, &s.CoinsEarned, &s.Streak,
			&s.Rank, &s.Total, &s.Ranked, &s.Provider, &s.Model,
			&attrRank, &boundDecisions, &loggedDecisions)
	if errors.Is(err, pgx.ErrNoRows) {
		return rating.Standing{}, false, nil
	}
	if err != nil {
		return rating.Standing{}, false, err
	}
	s.Verified = rating.NewCoverage(int(loggedDecisions), int(boundDecisions))
	s.Attribution = rating.Tier(attrRank, s.Verified)
	return s, true, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func resolveAgentID(ctx context.Context, tx pgx.Tx, agentPublicID string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM agents WHERE public_id = $1`, agentPublicID).Scan(&id)
	return id, err
}

func updateRating(ctx context.Context, tx pgx.Tx, agentID int64, game string, season int, nr rating.RatingState, oldStreak, w, l, t int, coins int64) error {
	streak := 0
	if w == 1 {
		streak = oldStreak + 1 // a win extends the streak; a loss/tie resets it
	}
	_, err := tx.Exec(ctx,
		`UPDATE ratings
		 SET elo = $4, rd = $5, vol = $6, mu = $7, sigma = $8,
		     wins = wins + $9, losses = losses + $10, ties = ties + $11,
		     coins_earned = coins_earned + $12, current_streak = $13,
		     best_streak = GREATEST(best_streak, $13), updated_at = now()
		 WHERE agent_id = $1 AND game = $2 AND season = $3`,
		agentID, game, season, nr.Elo, nr.RD, nr.Vol, nr.Mu, nr.Sigma, w, l, t, coins, streak)
	return err
}

// AgentElo returns the agent's rating for the season, defaulting to the 1500
// Glicko-2 baseline when the agent has not yet been rated this season (so unrated
// agents matchmake from the baseline rather than failing).
func (r *RatingRepo) AgentElo(ctx context.Context, agentPublicID, game string, season int) (int, error) {
	var elo int
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(
		     (SELECT rt.elo FROM ratings rt
		      JOIN agents a ON a.id = rt.agent_id
		      WHERE a.public_id = $1 AND rt.game = $3 AND rt.season = $2),
		     1500)`,
		agentPublicID, season, game).Scan(&elo)
	if err != nil {
		return 1500, err
	}
	return elo, nil
}

func (r *RatingRepo) LastRolledSeason(ctx context.Context) (int, error) {
	var season *int
	if err := r.db.QueryRow(ctx, `SELECT MAX(season) FROM season_rolls`).Scan(&season); err != nil {
		return -1, err
	}
	if season == nil {
		return -1, nil
	}
	return *season, nil
}

func (r *RatingRepo) RollSeason(ctx context.Context, season int, champion string) (bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after commit

	ct, err := tx.Exec(ctx,
		`INSERT INTO season_rolls (season, champion_agent_public_id) VALUES ($1, $2)
		 ON CONFLICT (season) DO NOTHING`,
		season, nullString(champion))
	if err != nil {
		return false, err
	}
	if ct.RowsAffected() == 0 {
		return false, nil // already rolled
	}

	// Emit season.rolled in the SAME tx (transactional outbox): champion badges +
	// "new season" notifications project off this.
	payload, err := json.Marshal(map[string]any{"season": season, "champion_agent_id": champion})
	if err != nil {
		return false, err
	}
	if _, err := InsertEventTx(ctx, tx, events.TypeSeasonRolled, payload); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// developerModelSplitSQL is every (developer, model) pairing's season record — the input
// to the skill-above-model edge.
//
// $1 season · $2 game (” = every arena) · $3 window start · $4 window end
//
// Built on the SAME resolved facts as the board, so a developer is scored against the
// exact model baseline the board publishes. Grouping by owner rather than by agent is
// the whole point: the question is which ENGINEER extracts the most from a model, and a
// developer running four agents on one model is one competitor, not four.
const developerModelSplitSQL = modelFactCTE + `
SELECT u.public_id, COALESCE(u.username::text,''), COALESCE(u.display_name,''), COALESCE(u.avatar_url,''),
       f.provider, f.model,
       COUNT(DISTINCT f.agent_id)::int                   AS agents,
       COUNT(*)::int                                     AS matches,
       COUNT(*) FILTER (WHERE f.result = 'win')::int     AS wins,
       COUNT(*) FILTER (WHERE f.result = 'loss')::int    AS losses,
       COUNT(*) FILTER (WHERE f.result = 'draw')::int    AS ties,
       COALESCE(SUM(f.tokens),0)::bigint                 AS tokens,
       COALESCE(SUM(f.estimated_cost),0)::double precision AS est_cost,
       COALESCE(SUM(f.verified_cost),0)::double precision  AS verified_cost,
       -- Verified coverage, so the cost columns below can tell a complete self-reported
       -- total from a partial gateway-observed slice. Without it a developer with one
       -- thinly-routed model and several unrouted ones produced a CostUSD that mixed the two
       -- and then divided it by every win.
       COALESCE(SUM(f.bound_decisions),0)::bigint          AS bound_decisions,
       COALESCE(SUM(f.logged_decisions),0)::bigint         AS logged_decisions
FROM fact f
JOIN agents a ON a.id = f.agent_id
JOIN users  u ON u.id = a.owner_user_id
WHERE f.model <> ''
GROUP BY u.public_id, u.username, u.display_name, u.avatar_url, f.provider, f.model`

// DeveloperModelSplit returns each developer's record on each model they ran.
func (r *RatingRepo) DeveloperModelSplit(ctx context.Context, season int, game string, start, end time.Time) ([]rating.DevModelRow, error) {
	_ = season // the [start, end) window already bounds the season
	rows, err := r.db.Query(ctx, developerModelSplitSQL, game, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rating.DevModelRow
	for rows.Next() {
		var d rating.DevModelRow
		var boundDecisions, loggedDecisions int64
		if err := rows.Scan(&d.UserPublicID, &d.Username, &d.DisplayName, &d.AvatarURL,
			&d.Provider, &d.Model, &d.Agents, &d.Matches, &d.Wins, &d.Losses, &d.Ties,
			&d.Tokens, &d.EstCostUSD, &d.VerifiedCostUSD,
			&boundDecisions, &loggedDecisions); err != nil {
			return nil, err
		}
		d.Verified = rating.NewCoverage(int(loggedDecisions), int(boundDecisions))
		out = append(out, d)
	}
	return out, rows.Err()
}
