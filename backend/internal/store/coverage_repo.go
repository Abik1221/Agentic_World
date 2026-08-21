package store

// Incremental refresh of the per-seat coverage rollup.
//
// # Why a rollup and not a live aggregate
//
// The two public benchmark endpoints used to compute verified coverage by grouping
// agent_match_decisions per request. Unfiltered, that is a scan of the entire decision history —
// measured at 10.2M rows and 23 GB — and it made both endpoints hang until the caller gave up.
// See migrations/0098 for why the cheaper-looking substitution was rejected.
//
// # Why incremental
//
// A full rebuild is the same 23 GB scan, just moved off the request path, and on a timer it would
// run forever against a table that only grows. Refresh instead advances a watermark over match
// FINISH time and only touches seats from matches that finished since. A finished match is
// immutable for this purpose, so a seat computed once never needs recomputing.
//
// The watermark is persisted rather than derived from max(computed_at) on the rollup: a row is
// only written when a seat actually has decisions, so a window containing nothing but empty
// matches would otherwise be rescanned on every tick and the refresh would never advance.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CoverageRepo maintains agent_match_coverage.
type CoverageRepo struct{ db *pgxpool.Pool }

func NewCoverageRepo(db *pgxpool.Pool) *CoverageRepo { return &CoverageRepo{db: db} }

// CoverageResult reports what one refresh did, for the log line and for tests.
type CoverageResult struct {
	Seats         int
	From, Through time.Time
	FullRebuild   bool
}

// Refresh brings the rollup up to date.
//
// batch bounds how far the watermark may advance in one call, in hours. A first run on a database
// with months of history would otherwise attempt the whole 23 GB in one statement and be killed by
// a statement timeout partway, leaving the watermark unmoved and the next tick repeating it. With
// a bound the backfill converges over several ticks and each one commits.
func (r *CoverageRepo) Refresh(ctx context.Context, batch time.Duration) (CoverageResult, error) {
	if batch <= 0 {
		batch = 24 * time.Hour
	}
	var from time.Time
	var full bool
	err := r.db.QueryRow(ctx,
		`SELECT covered_through FROM agent_match_coverage_watermark WHERE id = 1`).Scan(&from)
	if err != nil {
		// No watermark yet: start from the oldest finished match. NULL (an empty table) leaves
		// `from` zero, which the window below turns into a no-op rather than a full scan.
		full = true
		if err2 := r.db.QueryRow(ctx,
			`SELECT COALESCE(MIN(finished_at), now()) FROM matches WHERE finished_at IS NOT NULL`,
		).Scan(&from); err2 != nil {
			return CoverageResult{}, fmt.Errorf("coverage: read oldest match: %w", err2)
		}
		from = from.Add(-time.Second) // inclusive of the oldest match
	}
	through := from.Add(batch)
	if now := time.Now(); through.After(now) {
		through = now
	}
	if !through.After(from) {
		return CoverageResult{From: from, Through: from}, nil
	}

	tag, err := r.db.Exec(ctx, `
		WITH win AS (
		    SELECT public_id FROM matches
		     WHERE finished_at IS NOT NULL AND finished_at > $1 AND finished_at <= $2
		),
		logged AS (
		    SELECT d.match_id, d.agent_id, COUNT(*)::bigint AS n
		      FROM agent_match_decisions d JOIN win ON win.public_id = d.match_id
		     GROUP BY d.match_id, d.agent_id
		),
		bound AS (
		    SELECT d.match_id, d.agent_id, COUNT(DISTINCT d.round)::bigint AS n
		      FROM agent_match_bound_decisions d JOIN win ON win.public_id = d.match_id
		     GROUP BY d.match_id, d.agent_id
		)
		INSERT INTO agent_match_coverage (match_id, agent_id, logged_decisions, bound_decisions, computed_at)
		SELECT COALESCE(l.match_id, b.match_id), COALESCE(l.agent_id, b.agent_id),
		       COALESCE(l.n, 0), COALESCE(b.n, 0), now()
		  FROM logged l
		  FULL OUTER JOIN bound b ON b.match_id = l.match_id AND b.agent_id = l.agent_id
		ON CONFLICT (match_id, agent_id) DO UPDATE SET
		     logged_decisions = EXCLUDED.logged_decisions,
		     bound_decisions  = EXCLUDED.bound_decisions,
		     computed_at      = now()`, from, through)
	if err != nil {
		return CoverageResult{}, fmt.Errorf("coverage: refresh window: %w", err)
	}

	// The watermark moves only after the rows are in, and in the same call, so a failure mid-way
	// repeats the window rather than skipping it. Recomputing a seat is harmless; missing one is
	// a silently wrong published coverage figure.
	if _, err := r.db.Exec(ctx, `
		INSERT INTO agent_match_coverage_watermark (id, covered_through, updated_at)
		VALUES (1, $1, now())
		ON CONFLICT (id) DO UPDATE SET covered_through = EXCLUDED.covered_through, updated_at = now()`,
		through); err != nil {
		return CoverageResult{}, fmt.Errorf("coverage: advance watermark: %w", err)
	}
	return CoverageResult{
		Seats: int(tag.RowsAffected()), From: from, Through: through, FullRebuild: full,
	}, nil
}

// CoverageCaughtUp reports whether the rollup has reached the newest finished match.
//
// Exposed so a caller can tell "coverage is zero because nothing was bound" apart from "coverage
// is zero because the rollup has not reached these matches yet" — publishing the second as the
// first would understate every new model until the backfill caught up.
func (r *CoverageRepo) CoverageCaughtUp(ctx context.Context) (bool, error) {
	var behind bool
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(
		   (SELECT MAX(finished_at) FROM matches WHERE finished_at IS NOT NULL)
		   > (SELECT covered_through FROM agent_match_coverage_watermark WHERE id = 1),
		   true)`).Scan(&behind)
	return !behind, err
}
