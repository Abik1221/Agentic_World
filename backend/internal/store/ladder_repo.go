package store

// Persistence for the certified exploitability ladder (migration 0097).
//
// Every write here is idempotent on its natural key, because the caller is a worker that can
// die between playing matches and recording them. A replayed batch must be absorbed, not
// double-counted: a duplicated payoff would silently narrow a published bound, and a
// duplicated fit count would tilt the prober toward whichever batch was replayed.
//
// The schema does most of the enforcing — prober-pinned-before-certify, one open run per
// (agent, spec), payoff unique per match AND per seq, spec immutability by trigger. This file
// deliberately does not re-check those in Go. A control implemented in two places drifts, and
// the database is the one that cannot be bypassed by a second caller.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agent-arena/arena/internal/exploit"
	"github.com/agent-arena/arena/internal/gops"
	"github.com/agent-arena/arena/internal/ladder"
)

// LadderRepo implements ladder.Repo.
type LadderRepo struct{ db *pgxpool.Pool }

func NewLadderRepo(db *pgxpool.Pool) *LadderRepo { return &LadderRepo{db: db} }

// ErrNoActiveSpec is returned when no ladder is published. Distinct from a database failure:
// "nobody has published a ladder yet" is a normal pre-launch state, and a worker should skip
// rather than alert.
var ErrNoActiveSpec = errors.New("ladder: no active spec")

func (r *LadderRepo) ActiveSpec(ctx context.Context) (ladder.Spec, error) {
	var s ladder.Spec
	err := r.db.QueryRow(ctx, `
		SELECT version, n, prize_orders, tie_rule, alpha, delta, prober_mixture, target_precision, reference_opponent,
		       phase_a_matches, first_checkpoint, max_phase_b,
		       budget_ms, per_move_ms, increment_ms, grace_ms
		  FROM lab_ladder_spec WHERE active`).
		Scan(&s.Version, &s.N, &s.PrizeOrders, &s.TieRule, &s.Alpha, &s.Delta,
			&s.ProberMixture, &s.TargetPrecision, &s.ReferenceOpponent, &s.PhaseAMatches, &s.FirstCheckpoint, &s.MaxPhaseB,
			&s.BudgetMS, &s.PerMoveMS, &s.IncrementMS, &s.GraceMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return ladder.Spec{}, ErrNoActiveSpec
	}
	if err != nil {
		return ladder.Spec{}, err
	}
	// Validate on READ, not only on write. A row that predates a validation change, or that
	// was inserted by hand, must not silently drive a certification run.
	return s, s.Validate()
}

// PublishSpec appends a spec version and optionally makes it the active one.
//
// The hash is computed here from the struct rather than accepted from the caller, so a row's
// stored hash always describes the row's own values. Accepting one would let a caller publish
// a spec under someone else's identity.
func (r *LadderRepo) PublishSpec(ctx context.Context, s ladder.Spec, active bool) error {
	hash, err := s.Hash() // validates
	if err != nil {
		return err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if active {
		// The partial unique index allows exactly one active spec, so the incumbent stands
		// down inside the same transaction.
		//
		// EXCLUDING THIS VERSION. An earlier draft deactivated every active row and then
		// relied on the INSERT to set active = true — which silently RETIRED the ladder when
		// re-publishing a version that already existed, because ON CONFLICT DO NOTHING skips
		// the insert. The live repo test caught it on the second PublishSpec call.
		if _, err := tx.Exec(ctx,
			`UPDATE lab_ladder_spec SET active = false WHERE active AND version <> $1`,
			s.Version); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO lab_ladder_spec (version, n, prize_orders, tie_rule, alpha, delta,
		    prober_mixture, target_precision, reference_opponent, phase_a_matches,
		    first_checkpoint, max_phase_b, budget_ms, per_move_ms, increment_ms, grace_ms,
		    spec_hash, active)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (version) DO NOTHING`,
		s.Version, s.N, s.PrizeOrders, s.TieRule, s.Alpha, s.Delta,
		s.ProberMixture, s.TargetPrecision, s.ReferenceOpponent, s.PhaseAMatches, s.FirstCheckpoint, s.MaxPhaseB,
		s.BudgetMS, s.PerMoveMS, s.IncrementMS, s.GraceMS, hash, active); err != nil {
		return err
	}
	// DO NOTHING rather than DO UPDATE: re-publishing an existing version is a no-op, never
	// a rewrite. The immutability trigger would reject the rewrite anyway; this keeps a
	// re-run of a seeding job from erroring on a row it already wrote.
	//
	// `active` is set separately, and unconditionally, for exactly that reason: when the
	// insert is skipped it is the only statement that can still make this the live ladder.
	// It is also the one column the immutability trigger permits to change, because retiring
	// a spec must stay possible without altering what any certificate measured.
	if active {
		if _, err := tx.Exec(ctx,
			`UPDATE lab_ladder_spec SET active = true WHERE version = $1 AND NOT active`,
			s.Version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *LadderRepo) OpenRun(ctx context.Context, agentPublicID string, specVersion int) (ladder.Run, error) {
	// Create-if-absent, then read back. The partial unique index on open runs makes the
	// insert the arbiter under concurrency: two workers racing to start the same agent's run
	// leave exactly one row, and both then read it.
	if _, err := r.db.Exec(ctx, `
		INSERT INTO lab_cert_run (agent_id, spec_version)
		SELECT a.id, $2 FROM agents a WHERE a.public_id = $1
		ON CONFLICT DO NOTHING`, agentPublicID, specVersion); err != nil {
		return ladder.Run{}, err
	}
	var run ladder.Run
	var digest *string
	err := r.db.QueryRow(ctx, `
		SELECT r.id, a.public_id, r.spec_version, r.phase, r.fit_done, r.certify_done,
		       r.fit_kept, r.fit_offered, r.prober_digest
		  FROM lab_cert_run r JOIN agents a ON a.id = r.agent_id
		 WHERE a.public_id = $1 AND r.spec_version = $2 AND r.phase IN ('fit','certify')`,
		agentPublicID, specVersion).
		Scan(&run.ID, &run.AgentID, &run.SpecVersion, &run.Phase, &run.FitDone,
			&run.CertifyDone, &run.FitKept, &run.FitOffered, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return ladder.Run{}, fmt.Errorf("ladder: no open run for agent %s on spec v%d",
			agentPublicID, specVersion)
	}
	if err != nil {
		return ladder.Run{}, err
	}
	if digest != nil {
		run.ProberDigest = *digest
	}
	return run, nil
}

func (r *LadderRepo) AddFitCounts(ctx context.Context, runID int64, counts []exploit.OrderedCount, matchesPlayed int, census exploit.Census) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	for _, c := range counts {
		// ADD on conflict, not overwrite: a node is revisited across batches and across
		// matches, and replacing would discard every earlier observation at that node.
		batch.Queue(`
			INSERT INTO lab_fit_counts (run_id, prize_order, node_me, node_opp, node_carry, card, n)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (run_id, prize_order, node_me, node_opp, node_carry, card)
			DO UPDATE SET n = lab_fit_counts.n + EXCLUDED.n`,
			runID, c.Order, int(c.Node.Me), int(c.Node.Opp), c.Node.Carry, c.Card, c.N)
	}
	// Progress and census move in the SAME transaction as the counts. Splitting them would
	// let a crash advance the phase on a short sample, or publish a kept fraction that does
	// not describe the counts the prober was fitted on.
	batch.Queue(`
		UPDATE lab_cert_run
		   SET fit_done = fit_done + $2, fit_kept = fit_kept + $3,
		       fit_offered = fit_offered + $4, updated_at = now()
		 WHERE id = $1 AND phase = 'fit'`,
		runID, matchesPlayed, census.Kept, census.Total())

	br := tx.SendBatch(ctx, batch)
	if err := br.Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *LadderRepo) FitCounts(ctx context.Context, runID int64) ([]exploit.OrderedCount, error) {
	rows, err := r.db.Query(ctx, `
		SELECT prize_order, node_me, node_opp, node_carry, card, n
		  FROM lab_fit_counts WHERE run_id = $1`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []exploit.OrderedCount
	for rows.Next() {
		var ord, me, opp, carry, card, n int
		if err := rows.Scan(&ord, &me, &opp, &carry, &card, &n); err != nil {
			return nil, err
		}
		out = append(out, exploit.OrderedCount{Order: ord, Count: exploit.Count{
			Node: gops.Node{Me: uint16(me), Opp: uint16(opp), Carry: carry},
			Card: card, N: n,
		}})
	}
	return out, rows.Err()
}

func (r *LadderRepo) PinProber(ctx context.Context, runID int64, digest string) error {
	// Guarded on phase = 'fit' AND prober_digest IS NULL so the pin can happen exactly once,
	// even if two workers reach the transition together. The loser updates zero rows and its
	// EnterCertify will fail, which is the correct outcome: only one of them pinned.
	tag, err := r.db.Exec(ctx, `
		UPDATE lab_cert_run
		   SET phase = 'certify', prober_digest = $2, updated_at = now()
		 WHERE id = $1 AND phase = 'fit' AND prober_digest IS NULL`, runID, digest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("ladder: run %d was not in fit with an unpinned prober", runID)
	}
	return nil
}

func (r *LadderRepo) AddPayoffs(ctx context.Context, runID int64, p []ladder.MatchPayoff) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	for _, x := range p {
		// DO NOTHING on the match key: a replayed batch is absorbed. Counting a payoff twice
		// would narrow the bound with evidence that does not exist.
		batch.Queue(`
			INSERT INTO lab_certify_payoff (run_id, match_id, seq, payoff)
			VALUES ($1,$2,$3,$4) ON CONFLICT (run_id, match_id) DO NOTHING`,
			runID, x.MatchID, x.Seq, x.Payoff)
	}
	br := tx.SendBatch(ctx, batch)
	if err := br.Close(); err != nil {
		return err
	}
	// Derived from the table rather than incremented, so the counter cannot drift from the
	// rows a replayed batch did or did not insert.
	if _, err := tx.Exec(ctx, `
		UPDATE lab_cert_run
		   SET certify_done = (SELECT count(*) FROM lab_certify_payoff WHERE run_id = $1),
		       updated_at = now()
		 WHERE id = $1`, runID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Payoffs returns Phase B results in PLAY ORDER.
//
// Order is load-bearing: the sequential bound is read at a prefix, so returning them shuffled
// would certify a different sample than the one the stopping decision was made on.
func (r *LadderRepo) Payoffs(ctx context.Context, runID int64) ([]float64, error) {
	rows, err := r.db.Query(ctx, `
		SELECT payoff FROM lab_certify_payoff WHERE run_id = $1 ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *LadderRepo) SaveCertificate(ctx context.Context, c ladder.Certificate) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO lab_certificate (run_id, agent_id, spec_version, spec_hash, prober_digest,
		    games, mean_payoff, std_dev, lower_bound, delta, informative, stop_reason,
		    fit_matches, kept_fraction)
		SELECT $1, a.id, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
		  FROM agents a WHERE a.public_id = $14
		ON CONFLICT (run_id) DO NOTHING`,
		c.RunID, c.SpecVersion, c.SpecHash, c.ProberDigest, c.Games, c.MeanPayoff,
		c.StdDev, c.LowerBound, c.Delta, c.Informative, c.StopReason,
		c.FitMatches, c.KeptFraction, c.AgentID); err != nil {
		return err
	}
	// DO NOTHING, then close the run. A certificate is the terminal record of a run and must
	// never be rewritten: republishing a different number under the same run id is exactly
	// the failure the spec-immutability trigger exists to prevent, one level up.
	if _, err := tx.Exec(ctx, `
		UPDATE lab_cert_run SET phase = 'done', updated_at = now()
		 WHERE id = $1 AND phase = 'certify'`, c.RunID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ChainHead is the hash of the most recently published bundle, or "" for an empty chain.
//
// The empty string rather than an error on no rows: a platform that has published nothing has
// an empty chain, which is a normal state and not a failure. attest treats "" as the start of
// a chain.
func (r *LadderRepo) ChainHead(ctx context.Context) (string, error) {
	var h string
	err := r.db.QueryRow(ctx,
		`SELECT hash FROM lab_bundle_chain ORDER BY seq DESC LIMIT 1`).Scan(&h)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return h, err
}

// AppendChain records a published bundle's link.
//
// Idempotent on run_id: re-emitting a bundle for the same run must not fork the chain into two
// heads, which would make every later link ambiguous and the whole series unverifiable.
func (r *LadderRepo) AppendChain(ctx context.Context, runID int64, prevHash, hash string) error {
	if hash == "" {
		return fmt.Errorf("ladder: refusing to chain a bundle with no hash")
	}
	_, err := r.db.Exec(ctx, `
		INSERT INTO lab_bundle_chain (run_id, prev_hash, hash)
		VALUES ($1, $2, $3) ON CONFLICT (run_id) DO NOTHING`, runID, prevHash, hash)
	return err
}
