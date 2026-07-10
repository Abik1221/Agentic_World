package store

import (
	"context"

	"github.com/agent-arena/arena/internal/gamestakes"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GameStakesRepo is the pgx implementation of gamestakes.Repo.
type GameStakesRepo struct{ db *pgxpool.Pool }

func NewGameStakesRepo(db *pgxpool.Pool) *GameStakesRepo { return &GameStakesRepo{db: db} }

var _ gamestakes.Repo = (*GameStakesRepo)(nil)

func (r *GameStakesRepo) ListTiers(ctx context.Context, game string) ([]gamestakes.Tier, error) {
	rows, err := r.db.Query(ctx,
		`SELECT tier_key, label, coins, ordering, enabled
		 FROM game_stakes WHERE game = $1 ORDER BY ordering, coins`, game)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gamestakes.Tier
	for rows.Next() {
		var t gamestakes.Tier
		if err := rows.Scan(&t.Key, &t.Label, &t.Coins, &t.Ordering, &t.Enabled); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ReplaceTiers atomically swaps a game's whole tier set (delete + re-insert) so an
// admin PUT is all-or-nothing.
func (r *GameStakesRepo) ReplaceTiers(ctx context.Context, game string, tiers []gamestakes.Tier) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after commit

	if _, err := tx.Exec(ctx, `DELETE FROM game_stakes WHERE game = $1`, game); err != nil {
		return err
	}
	for _, t := range tiers {
		if _, err := tx.Exec(ctx,
			`INSERT INTO game_stakes (game, tier_key, label, coins, ordering, enabled)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			game, t.Key, t.Label, t.Coins, t.Ordering, t.Enabled); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *GameStakesRepo) Audit(ctx context.Context, actor, action, target string, detail []byte) error {
	d := string(detail)
	if d == "" {
		d = "{}"
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO audit_log (actor, action, target, detail) VALUES ($1, $2, $3, $4::jsonb)`,
		actor, action, nullString(target), d)
	return err
}
