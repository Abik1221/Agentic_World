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
	// The match is RESOLVED, not discarded.
	//
	// This used to read `_ = matchPublicID` under the comment "match_id stays NULL until the
	// matches table exists (Stage 3 resolves it)". The matches table does exist, and so does
	// the foreign key — agent_timing_samples.fk_timing_match references matches(id). So every
	// caller was passing a match id that was silently thrown away, and the column it was
	// meant to fill sat NULL on every one of the thousands of rows already written.
	//
	// That is not cosmetic. These samples ARE the timing profile that decides whether a human
	// is playing a match by hand, and with no match id there is no way to ask the question
	// that actually diagnoses a bad measurement: "what did this agent's think-times look like
	// in THAT match?" It made a wrong-think-time bug take a live poll to find, because the
	// samples could not be joined to the match that produced them.
	//
	// LEFT JOIN, not an inner one: a nil or unknown match must still record the sample. The
	// timing profile is the point, and losing a real response time because a match row was
	// not found would let a lookup failure quietly shrink the evidence a fraud control reads.
	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_timing_samples (agent_id, match_id, response_ms)
		 SELECT a.id, m.id, $3
		   FROM agents a
		   LEFT JOIN matches m ON m.public_id = $2
		  WHERE a.public_id = $1`,
		agentPublicID, matchPublicID, responseMs)
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
