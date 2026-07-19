package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StyleRepo persists per-agent, per-game behavioral style aggregates
// (agent_game_style). Write side satisfies match.StyleRecorder; read side feeds
// the public profile. Read-only descriptive metrics — never gates play or money.
type StyleRepo struct{ db *pgxpool.Pool }

func NewStyleRepo(db *pgxpool.Pool) *StyleRepo { return &StyleRepo{db: db} }

// RecordStyle folds one finished match's (aggression, efficiency) into the agent's
// rolling per-game sums. Idempotency is not required — each match contributes once
// (finalize runs once per match). A missing agent row makes this a no-op error,
// which the best-effort caller swallows.
func (r *StyleRepo) RecordStyle(ctx context.Context, agentPublicID, game string, aggression, efficiency int) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_game_style (agent_id, game, matches, aggression_sum, efficiency_sum, updated_at)
		 VALUES ((SELECT id FROM agents WHERE public_id = $1), $2, 1, $3, $4, now())
		 ON CONFLICT (agent_id, game) DO UPDATE SET
		   matches        = agent_game_style.matches + 1,
		   aggression_sum = agent_game_style.aggression_sum + EXCLUDED.aggression_sum,
		   efficiency_sum = agent_game_style.efficiency_sum + EXCLUDED.efficiency_sum,
		   updated_at     = now()`,
		agentPublicID, game, aggression, efficiency)
	return err
}

// AgentStyle returns the agent's average aggression + efficiency for a game (0-100
// each), and whether any samples exist. Zero values + ok=false when never recorded.
func (r *StyleRepo) AgentStyle(ctx context.Context, agentPublicID, game string) (aggression, efficiency int, ok bool, err error) {
	var matches, aggSum, effSum int
	err = r.db.QueryRow(ctx,
		`SELECT matches, aggression_sum, efficiency_sum FROM agent_game_style
		 WHERE agent_id = (SELECT id FROM agents WHERE public_id = $1) AND game = $2`,
		agentPublicID, game).Scan(&matches, &aggSum, &effSum)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	if matches <= 0 {
		return 0, 0, false, nil
	}
	return aggSum / matches, effSum / matches, true, nil
}
