package store

import (
	"context"
	"errors"

	"github.com/agent-arena/arena/internal/badges"
	"github.com/jackc/pgx/v5"
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

// AwardDeveloper inserts a developer badge idempotently (via the (user, code) PK).
func (r *BadgesRepo) AwardDeveloper(ctx context.Context, userPublicID, code string) (bool, error) {
	ct, err := r.db.Exec(ctx,
		`INSERT INTO developer_badges (user_id, code)
		 SELECT id, $2 FROM users WHERE public_id = $1
		 ON CONFLICT (user_id, code) DO NOTHING`, userPublicID, code)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}

// OwnerOf resolves an agent's owning developer (public id).
func (r *BadgesRepo) OwnerOf(ctx context.Context, agentPublicID string) (string, bool, error) {
	var owner string
	err := r.db.QueryRow(ctx,
		`SELECT u.public_id FROM agents a JOIN users u ON u.id = a.owner_user_id
		 WHERE a.public_id = $1`, agentPublicID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return owner, true, nil
}

// DeveloperRank returns a developer's P-Index rank + percentile for a season.
func (r *BadgesRepo) DeveloperRank(ctx context.Context, userPublicID string, season int) (int, float64, bool, error) {
	var rank int
	var pct float64
	err := r.db.QueryRow(ctx,
		`SELECT global_rank, percentile FROM developer_pindex
		 WHERE user_id = (SELECT id FROM users WHERE public_id = $1) AND season = $2`,
		userPublicID, season).Scan(&rank, &pct)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	return rank, pct, true, nil
}

// DeveloperTotals returns a developer's aggregate wins + best streak for a season.
func (r *BadgesRepo) DeveloperTotals(ctx context.Context, userPublicID string, season int) (int, int, error) {
	var wins, best int
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(r.wins),0), COALESCE(MAX(r.best_streak),0)
		 FROM ratings r JOIN agents a ON a.id = r.agent_id
		 WHERE a.owner_user_id = (SELECT id FROM users WHERE public_id = $1)
		   AND r.season = $2 AND a.kind = 'external'`, userPublicID, season).Scan(&wins, &best)
	if err != nil {
		return 0, 0, err
	}
	return wins, best, nil
}
