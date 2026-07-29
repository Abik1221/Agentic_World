package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
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
		   WHERE r.game = $1 AND r.season = $2 AND a.kind <> 'house'
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

// SnapshotRanks records the current rank of every non-house agent per (game,
// season) for the given day. Idempotent per day via the UNIQUE(taken_on) key, so
// running it more than once a day (or on multiple instances) is safe.
func (r *RatingRepo) SnapshotRanks(ctx context.Context, takenOn time.Time) (int, error) {
	tag, err := r.db.Exec(ctx,
		`INSERT INTO rating_rank_snapshots (game, season, agent_id, rank, taken_on)
		 SELECT r.game, r.season, r.agent_id,
		        RANK() OVER (PARTITION BY r.game, r.season ORDER BY r.elo DESC, r.agent_id ASC),
		        $1::date
		 FROM ratings r JOIN agents a ON a.id = r.agent_id
		 WHERE a.kind <> 'house'
		 ON CONFLICT (game, season, agent_id, taken_on) DO NOTHING`, takenOn)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ModelBenchmark groups the season's rated agents by their DECLARED model (the
// latest non-rejected manifest per agent) and aggregates games/elo/coins. Only
// models with >= minGames total games are returned, best avg-ELO first.
func (r *RatingRepo) ModelBenchmark(ctx context.Context, season int, game string, minGames int) ([]rating.ModelStat, error) {
	rows, err := r.db.Query(ctx,
		`WITH mdl AS (
		   SELECT DISTINCT ON (agent_public_id) agent_public_id,
		          model_provider AS provider, model_name AS model
		   FROM agent_manifests
		   WHERE status <> 'rejected'
		     AND COALESCE(model_provider,'') <> '' AND COALESCE(model_name,'') <> ''
		   ORDER BY agent_public_id, created_at DESC
		 ),
		 bench AS (
		   -- Joined to matches for REAL wall-clock game time. Tokens-per-minute is the
		   -- headline efficiency number on the public models board, and dividing by
		   -- thinking time instead would answer a different question (throughput while
		   -- deciding) under a label that promises game time. Only finished matches
		   -- with a sane duration contribute, so a stuck or aborted match cannot
		   -- inflate the denominator to near-zero and produce an absurd rate.
		   SELECT b.agent_id,
		          SUM(b.latency_sum_ms) AS lat_sum,
		          SUM(b.decisions)      AS decisions,
		          SUM(b.tokens)         AS tokens,
		          SUM(b.estimated_cost) AS cost,
		          SUM(b.legal)          AS legal,
		          SUM(b.fallbacks)      AS fallbacks,
		          SUM(EXTRACT(EPOCH FROM (m.finished_at - m.started_at))) FILTER (
		            WHERE m.finished_at IS NOT NULL AND m.started_at IS NOT NULL
		              AND m.finished_at > m.started_at
		          ) AS play_seconds
		   FROM agent_match_benchmark b
		   LEFT JOIN matches m ON m.public_id = b.match_id
		   WHERE b.game = $2
		   GROUP BY b.agent_id
		 )
		 SELECT mdl.provider, mdl.model,
		        COUNT(*)::int                              AS agents,
		        COALESCE(SUM(r.wins),0)::int               AS wins,
		        COALESCE(SUM(r.losses),0)::int             AS losses,
		        COALESCE(SUM(r.ties),0)::int               AS ties,
		        COALESCE(ROUND(AVG(r.elo)),0)::int         AS avg_elo,
		        COALESCE(SUM(r.coins_earned),0)::bigint    AS coins_won,
		        COALESCE(ROUND(SUM(b.lat_sum) / NULLIF(SUM(b.decisions),0)),0)::int AS avg_latency_ms,
		        COALESCE(SUM(b.cost),0)::double precision  AS est_cost_usd,
		        COALESCE(SUM(b.tokens),0)::bigint          AS tokens,
		        COALESCE(SUM(b.legal),0)::bigint           AS legal,
		        COALESCE(SUM(b.fallbacks),0)::bigint       AS fallbacks,
		        COALESCE(SUM(b.decisions),0)::bigint       AS decisions,
		        COALESCE(SUM(b.play_seconds),0)::double precision AS play_seconds
		 FROM mdl
		 JOIN agents  a ON a.public_id = mdl.agent_public_id AND a.kind <> 'house'
		 JOIN ratings r ON r.agent_id = a.id AND r.game = $2 AND r.season = $1
		 LEFT JOIN bench b ON b.agent_id = a.id
		 GROUP BY mdl.provider, mdl.model
		 HAVING (COALESCE(SUM(r.wins),0)+COALESCE(SUM(r.losses),0)+COALESCE(SUM(r.ties),0)) >= $3
		 ORDER BY avg_elo DESC, coins_won DESC`, season, game, minGames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rating.ModelStat
	for rows.Next() {
		var s rating.ModelStat
		if err := rows.Scan(&s.Provider, &s.Model, &s.Agents, &s.Wins, &s.Losses, &s.Ties, &s.AvgElo, &s.CoinsWon,
			&s.AvgLatencyMs, &s.EstCostUSD, &s.Tokens,
			&s.Legal, &s.Fallbacks, &s.Decisions, &s.PlaySeconds); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AgentStanding returns an agent's rank + totals for the season. Rank is 1-based,
// ordered by ELO desc (ties broken by lower agent_id, matching the leaderboard).
func (r *RatingRepo) AgentStanding(ctx context.Context, season int, game, agentPublicID string) (rating.Standing, bool, error) {
	var s rating.Standing
	s.Season = season
	s.Game = game
	s.AgentPublicID = agentPublicID
	err := r.db.QueryRow(ctx,
		`SELECT a.name, r.elo, r.wins, r.losses, r.ties, r.coins_earned, r.current_streak,
		   (SELECT COUNT(*)+1 FROM ratings r2 JOIN agents a2 ON a2.id = r2.agent_id
		      WHERE r2.game = $3 AND r2.season = $1 AND a2.kind <> 'house'
		        AND (r2.elo > r.elo OR (r2.elo = r.elo AND r2.agent_id < r.agent_id))) AS rank,
		   (SELECT COUNT(*) FROM ratings r3 JOIN agents a3 ON a3.id = r3.agent_id
		      WHERE r3.game = $3 AND r3.season = $1 AND a3.kind <> 'house') AS total,
		   COALESCE((SELECT model_provider FROM agent_manifests m WHERE m.agent_id = a.id
		             AND m.status <> 'rejected' ORDER BY created_at DESC LIMIT 1), ''),
		   COALESCE((SELECT model_name FROM agent_manifests m WHERE m.agent_id = a.id
		             AND m.status <> 'rejected' ORDER BY created_at DESC LIMIT 1), '')
		 FROM ratings r JOIN agents a ON a.id = r.agent_id
		 WHERE r.game = $3 AND r.season = $1 AND a.public_id = $2`,
		season, agentPublicID, game).
		Scan(&s.Name, &s.Elo, &s.Wins, &s.Losses, &s.Ties, &s.CoinsEarned, &s.Streak,
			&s.Rank, &s.Total, &s.Provider, &s.Model)
	if errors.Is(err, pgx.ErrNoRows) {
		return rating.Standing{}, false, nil
	}
	if err != nil {
		return rating.Standing{}, false, err
	}
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
