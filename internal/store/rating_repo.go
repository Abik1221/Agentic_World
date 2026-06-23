package store

import (
	"context"
	"sort"

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
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Idempotency marker: present ⇒ already rated. Bound to a real match via FK.
	ct, err := tx.Exec(ctx,
		`INSERT INTO rating_updates (match_id, season)
		 SELECT m.id, $2 FROM matches m WHERE m.public_id = $1
		 ON CONFLICT (match_id) DO NOTHING`, in.MatchPublicID, in.Season)
	if err != nil {
		return false, err
	}
	if ct.RowsAffected() == 0 {
		return false, tx.Commit(ctx) // already rated (or no such match): no-op
	}

	idA, err := resolveAgentID(ctx, tx, in.Agents[0])
	if err != nil {
		return false, err
	}
	idB, err := resolveAgentID(ctx, tx, in.Agents[1])
	if err != nil {
		return false, err
	}

	// Ensure both rating rows exist (default 1200) before locking them.
	for _, ag := range in.Agents {
		if _, err := tx.Exec(ctx,
			`INSERT INTO ratings (agent_id, season) SELECT id, $2 FROM agents WHERE public_id = $1
			 ON CONFLICT DO NOTHING`, ag, in.Season); err != nil {
			return false, err
		}
	}

	// Lock both rows in ascending id order to avoid deadlocks between concurrent
	// matches that share an agent.
	ids := []int64{idA, idB}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	elo := map[int64]int{}
	streak := map[int64]int{}
	rows, err := tx.Query(ctx,
		`SELECT agent_id, elo, current_streak FROM ratings
		 WHERE agent_id = ANY($1) AND season = $2 ORDER BY agent_id FOR UPDATE`, ids, in.Season)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var id int64
		var e, s int
		if err := rows.Scan(&id, &e, &s); err != nil {
			rows.Close()
			return false, err
		}
		elo[id], streak[id] = e, s
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	newA, newB := in.Compute(elo[idA], elo[idB])
	if err := updateRating(ctx, tx, idA, in.Season, 0, in.WinnerSeat, newA, streak[idA], in.CoinsDelta[0]); err != nil {
		return false, err
	}
	if err := updateRating(ctx, tx, idB, in.Season, 1, in.WinnerSeat, newB, streak[idB], in.CoinsDelta[1]); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (r *RatingRepo) Leaderboard(ctx context.Context, season, offset, limit int) ([]rating.LeaderRow, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id, a.slug, a.name, r.elo, r.wins, r.losses, r.ties, r.coins_earned, r.current_streak
		 FROM ratings r JOIN agents a ON a.id = r.agent_id
		 WHERE r.season = $1
		 ORDER BY r.elo DESC, r.agent_id ASC
		 LIMIT $2 OFFSET $3`, season, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rating.LeaderRow
	for rows.Next() {
		var lr rating.LeaderRow
		if err := rows.Scan(&lr.AgentPublicID, &lr.Slug, &lr.Name, &lr.Elo,
			&lr.Wins, &lr.Losses, &lr.Ties, &lr.CoinsEarned, &lr.Streak); err != nil {
			return nil, err
		}
		out = append(out, lr)
	}
	return out, rows.Err()
}

// ── helpers ──────────────────────────────────────────────────────────────────

func resolveAgentID(ctx context.Context, tx pgx.Tx, agentPublicID string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM agents WHERE public_id = $1`, agentPublicID).Scan(&id)
	return id, err
}

func updateRating(ctx context.Context, tx pgx.Tx, agentID int64, season, seat, winnerSeat, newElo, oldStreak int, coins int64) error {
	var w, l, t, streak int
	switch {
	case winnerSeat == rating.Tie:
		t, streak = 1, 0
	case winnerSeat == seat:
		w, streak = 1, oldStreak+1
	default:
		l, streak = 1, 0
	}
	_, err := tx.Exec(ctx,
		`UPDATE ratings
		 SET elo = $3, wins = wins + $4, losses = losses + $5, ties = ties + $6,
		     coins_earned = coins_earned + $7, current_streak = $8,
		     best_streak = GREATEST(best_streak, $8), updated_at = now()
		 WHERE agent_id = $1 AND season = $2`,
		agentID, season, newElo, w, l, t, coins, streak)
	return err
}
