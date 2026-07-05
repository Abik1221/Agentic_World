package store

import (
	"context"

	"github.com/agent-arena/arena/internal/badges"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BadgesRepo is the pgx implementation of badges.Repo.
type BadgesRepo struct{ db *pgxpool.Pool }

func NewBadgesRepo(db *pgxpool.Pool) *BadgesRepo { return &BadgesRepo{db: db} }

var _ badges.Repo = (*BadgesRepo)(nil)

// Award inserts the badge, returning whether it was newly granted (idempotent via
// the (agent, code) primary key).
func (r *BadgesRepo) Award(ctx context.Context, agentPublicID, code string) (bool, error) {
	ct, err := r.db.Exec(ctx,
		`INSERT INTO agent_badges (agent_public_id, code) VALUES ($1, $2)
		 ON CONFLICT (agent_public_id, code) DO NOTHING`,
		agentPublicID, code)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}
