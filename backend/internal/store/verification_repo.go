package store

import (
	"context"

	"github.com/agent-arena/arena/internal/verification"
	"github.com/jackc/pgx/v5/pgxpool"
)

// VerificationRepo is the pgx-backed implementation of verification.Repo.
type VerificationRepo struct{ db *pgxpool.Pool }

func NewVerificationRepo(db *pgxpool.Pool) *VerificationRepo { return &VerificationRepo{db: db} }

var _ verification.Repo = (*VerificationRepo)(nil)

func (r *VerificationRepo) InsertSample(ctx context.Context, agentPublicID string, matchPublicID *string, responseMs int) error {
	// match_id stays NULL until the matches table exists (Stage 3 resolves it).
	_ = matchPublicID
	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_timing_samples (agent_id, match_id, response_ms)
		 SELECT id, NULL, $2 FROM agents WHERE public_id = $1`,
		agentPublicID, responseMs)
	return err
}

func (r *VerificationRepo) RecentSamples(ctx context.Context, agentPublicID string, limit int) ([]int, error) {
	rows, err := r.db.Query(ctx,
		`SELECT ats.response_ms
		 FROM agent_timing_samples ats
		 JOIN agents a ON a.id = ats.agent_id
		 WHERE a.public_id = $1
		 ORDER BY ats.created_at DESC
		 LIMIT $2`, agentPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []int
	for rows.Next() {
		var ms int
		if err := rows.Scan(&ms); err != nil {
			return nil, err
		}
		out = append(out, ms)
	}
	return out, rows.Err()
}
