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

// RecentSamples returns the agent's most recent response times, EXCLUDING anything from
// before its last verification review.
//
// The exclusion is what makes "flagged for review" reviewable. The verdict is computed
// from an agent's own samples, samples are only produced by playing, and a flagged agent
// may not play — so without this a flag is permanent and a false positive (a provider
// outage, a rate limit, an unset API key: twenty slow, erratic turns) ends the agent.
//
// A review does not exempt the agent. It marks an instant and the detector starts again
// from there, so an agent that really is a person at a keyboard is flagged again twenty
// moves later by the same rule. The samples themselves are kept — the review is recorded
// beside the evidence, not instead of it.
//
// No review, no subquery effect: the NOT EXISTS is false for every agent that has never
// been reviewed, which is almost all of them, and the plan is the same index scan as
// before.
func (r *VerificationRepo) RecentSamples(ctx context.Context, agentPublicID string, limit int) ([]int, error) {
	rows, err := r.db.Query(ctx,
		`SELECT ats.response_ms
		 FROM agent_timing_samples ats
		 JOIN agents a ON a.id = ats.agent_id
		 WHERE a.public_id = $1
		   AND ats.created_at > COALESCE(
		         (SELECT max(vr.created_at)
		            FROM agent_verification_reviews vr
		           WHERE vr.agent_id = a.id),
		         '-infinity'::timestamptz)
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

// Review records an operator's decision to judge this agent from now on.
//
// Append-only and deliberately so. Erasing agent_timing_samples would destroy the record
// of why the agent was ever flagged, which is the evidence a later dispute needs; this
// writes the decision beside it instead. Reviewing twice is harmless — RecentSamples reads
// the newest row — so an operator who clicks again after a second false positive gets the
// obvious behaviour rather than an error.
func (r *VerificationRepo) Review(ctx context.Context, agentPublicID, reviewedBy, reason string) error {
	tag, err := r.db.Exec(ctx,
		`INSERT INTO agent_verification_reviews (agent_id, reviewed_by, reason)
		 SELECT a.id, $2, $3 FROM agents a WHERE a.public_id = $1`,
		agentPublicID, reviewedBy, reason)
	if err != nil {
		return err
	}
	// An unknown agent inserts nothing. Reported rather than swallowed: a silent success
	// would tell an operator the agent was cleared when the id was simply wrong, and they
	// would go on believing a flagged agent had been released.
	if tag.RowsAffected() == 0 {
		return verification.ErrAgentNotFound
	}
	return nil
}
