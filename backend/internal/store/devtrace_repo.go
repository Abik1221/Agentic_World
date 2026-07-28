package store

import (
	"context"

	"github.com/agent-arena/arena/internal/devtrace"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DevTraceRepo answers the one question the trace read path needs from Postgres:
// which agents does this user own?
type DevTraceRepo struct{ db *pgxpool.Pool }

func NewDevTraceRepo(db *pgxpool.Pool) *DevTraceRepo { return &DevTraceRepo{db: db} }

var _ devtrace.Repo = (*DevTraceRepo)(nil)

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
		   AND a.kind <> 'house'
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
