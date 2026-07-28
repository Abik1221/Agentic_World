package store

import (
	"context"
	"errors"
	"time"

	"github.com/agent-arena/arena/internal/liveness"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LivenessRepo is the pgx implementation of liveness.Repo.
type LivenessRepo struct{ db *pgxpool.Pool }

func NewLivenessRepo(db *pgxpool.Pool) *LivenessRepo { return &LivenessRepo{db: db} }

var _ liveness.Repo = (*LivenessRepo)(nil)

// LastBeat reads the single heartbeat row.
func (r *LivenessRepo) LastBeat(ctx context.Context) (time.Time, bool, error) {
	var at time.Time
	err := r.db.QueryRow(ctx, `SELECT beat_at FROM platform_liveness WHERE id = TRUE`).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return at, true, nil
}

// Beat records that this instance is serving.
//
// GREATEST() keeps the row monotonic: with several instances beating, a straggler
// whose clock is behind must never drag the heartbeat backwards and manufacture a
// phantom outage on the next boot.
func (r *LivenessRepo) Beat(ctx context.Context, at time.Time) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO platform_liveness (id, beat_at) VALUES (TRUE, $1)
		 ON CONFLICT (id) DO UPDATE SET beat_at = GREATEST(platform_liveness.beat_at, EXCLUDED.beat_at)`,
		at)
	return err
}

// RecordOutage appends the audit row for a detected gap.
func (r *LivenessRepo) RecordOutage(ctx context.Context, startedAt, detectedAt, graceUntil time.Time, gapSeconds int64) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO platform_outages (started_at, detected_at, grace_until, gap_seconds)
		 VALUES ($1, $2, $3, $4)`,
		startedAt, detectedAt, graceUntil, gapSeconds)
	return err
}
