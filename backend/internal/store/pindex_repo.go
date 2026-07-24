package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/agent-arena/arena/internal/events"
	"github.com/agent-arena/arena/internal/pindex"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PIndexRepo is the pgx implementation of pindex.Repo. It assembles a developer's
// scoring inputs (best agent per arena, match totals, opponent strength) and
// persists computed results transactionally with the pindex.updated event.
type PIndexRepo struct{ db *pgxpool.Pool }

func NewPIndexRepo(db *pgxpool.Pool) *PIndexRepo { return &PIndexRepo{db: db} }

var _ pindex.Repo = (*PIndexRepo)(nil)

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
		 WHERE a.owner_user_id = $1 AND r.season = $2 AND a.kind <> 'house'
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
		 WHERE a.owner_user_id = $1 AND mrc.season = $2 AND a.kind <> 'house'
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
		 JOIN agents sa ON sa.id = self.agent_id AND sa.owner_user_id = $1 AND sa.kind <> 'house'
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
		 JOIN agents a ON a.id = amb.agent_id AND a.owner_user_id = $1 AND a.kind <> 'house'
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

	return in, nil
}

// RecordMatchBenchmark upserts one seat's per-match decision-quality counts (the
// P-Index Intelligence projection over match.benchmark). A no-op when the agent's
// public id is unknown (INSERT…SELECT yields no row) so it never errors on a bot.
func (r *PIndexRepo) RecordMatchBenchmark(ctx context.Context, matchID, agentPublicID, game string, decisions, legal, fallbacks int, latencySumMS, tokens int64, estimatedCost float64, result string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_match_benchmark (match_id, agent_id, game, decisions, legal, fallbacks, latency_sum_ms, tokens, estimated_cost, result, updated_at)
		 SELECT $1, a.id, $3, $4, $5, $6, $7, $8, $9, $10, now() FROM agents a WHERE a.public_id = $2
		 ON CONFLICT (match_id, agent_id) DO UPDATE SET
		   game = EXCLUDED.game, decisions = EXCLUDED.decisions, legal = EXCLUDED.legal, fallbacks = EXCLUDED.fallbacks,
		   latency_sum_ms = EXCLUDED.latency_sum_ms, tokens = EXCLUDED.tokens,
		   estimated_cost = EXCLUDED.estimated_cost, result = EXCLUDED.result, updated_at = now()`,
		matchID, agentPublicID, game, decisions, legal, fallbacks, latencySumMS, tokens, estimatedCost, result)
	return err
}

// RecordVerifiedCost accumulates one gateway-observed LLM call's USD cost into the
// per-(match, agent) verified-cost row (server-measured, unfakeable). A no-op when the
// agent public id is unknown (INSERT…SELECT yields no row). matchID may be empty
// (call made outside a match) — such rows still aggregate per agent for lifetime cost.
func (r *PIndexRepo) RecordVerifiedCost(ctx context.Context, matchID, agentPublicID string, costUSD float64) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_match_verified_cost (match_id, agent_id, verified_cost, calls, updated_at)
		 SELECT $1, a.id, $3, 1, now() FROM agents a WHERE a.public_id = $2
		 ON CONFLICT (match_id, agent_id) DO UPDATE SET
		   verified_cost = agent_match_verified_cost.verified_cost + EXCLUDED.verified_cost,
		   calls = agent_match_verified_cost.calls + 1, updated_at = now()`,
		matchID, agentPublicID, costUSD)
	return err
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
		    highest_pindex, best_rank, config_version, computed_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$3,0,$9,$10)
		 ON CONFLICT (user_id, season) DO UPDATE SET
		   p_index = EXCLUDED.p_index, arena_c = EXCLUDED.arena_c,
		   consistency_c = EXCLUDED.consistency_c, difficulty_c = EXCLUDED.difficulty_c,
		   activity_c = EXCLUDED.activity_c, intelligence_c = EXCLUDED.intelligence_c,
		   highest_pindex = GREATEST(developer_pindex.highest_pindex, EXCLUDED.p_index),
		   config_version = EXCLUDED.config_version, computed_at = EXCLUDED.computed_at`,
		uid, season, res.PIndex, res.Sub("arena"), res.Sub("consistency"),
		res.Sub("difficulty"), res.Sub("activity"), res.Sub("intelligence"), res.ConfigVersion, asOf); err != nil {
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

func (r *PIndexRepo) Rank(ctx context.Context, season int) error {
	_, err := r.db.Exec(ctx,
		`WITH ranked AS (
		   SELECT user_id,
		          ROW_NUMBER() OVER (ORDER BY p_index DESC, user_id) AS rnk,
		          COUNT(*)     OVER ()                               AS total
		   FROM developer_pindex WHERE season = $1
		 )
		 UPDATE developer_pindex d SET
		   global_rank = r.rnk,
		   percentile  = CASE WHEN r.total > 0 THEN ROUND(100.0 * r.rnk / r.total, 2) ELSE 0 END,
		   best_rank   = CASE WHEN d.best_rank = 0 THEN r.rnk ELSE LEAST(d.best_rank, r.rnk) END
		 FROM ranked r WHERE d.user_id = r.user_id AND d.season = $1`, season)
	return err
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
