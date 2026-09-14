package store

import (
	"context"
	"encoding/json"

	"github.com/agent-arena/arena/internal/paymenttrace"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PaymentTraceRepo is the pgx implementation of paymenttrace.Repo — the payment
// log (migration 0069).
type PaymentTraceRepo struct{ db *pgxpool.Pool }

func NewPaymentTraceRepo(db *pgxpool.Pool) *PaymentTraceRepo { return &PaymentTraceRepo{db: db} }

var _ paymenttrace.Repo = (*PaymentTraceRepo)(nil)

// Record upserts one stage of one payment attempt.
//
// Two properties the WHERE clause on the conflict path buys, both of which matter
// more than they look:
//
//   - A poller re-reporting an unchanged state does not touch the row. The deposit
//     listener runs every 15 seconds against every open session; without this, `at`
//     would advance on every tick and the stall detector — which is literally
//     "nothing has changed since X" — could never fire.
//   - `attempts` counts real transitions, so a stage showing attempts=6 means six
//     genuine retries, not six ticks of a timer.
//
// The user is resolved by public_id in the same statement; an unknown user inserts
// nothing rather than erroring, because a trace row is never worth failing a
// money path over.
func (r *PaymentTraceRepo) Record(ctx context.Context, userPublicID string, e paymenttrace.Event) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO payment_events (user_id, flow, ref, stage, status, detail, metadata)
		 SELECT u.id, $2, $3, $4, $5, $6, $7::jsonb FROM users u WHERE u.public_id = $1
		 ON CONFLICT (flow, ref, stage) DO UPDATE
		   SET status   = EXCLUDED.status,
		       detail   = EXCLUDED.detail,
		       metadata = EXCLUDED.metadata,
		       at       = now(),
		       attempts = payment_events.attempts + 1
		 WHERE payment_events.status IS DISTINCT FROM EXCLUDED.status
		    OR payment_events.detail IS DISTINCT FROM EXCLUDED.detail`,
		userPublicID, e.Flow, e.Ref, e.Stage, e.Status, e.Detail,
		string(paymenttrace.MarshalMetadata(e.Metadata)))
	return err
}

// ByUser returns every event belonging to the user's `limit` most recent flows.
//
// It pages by FLOW, not by row: a LIMIT over raw events would slice a timeline in
// half at the boundary and render a payment as missing its last two stages — the
// exact false alarm this feature exists to remove.
func (r *PaymentTraceRepo) ByUser(ctx context.Context, userPublicID string, limit int) ([]paymenttrace.Event, error) {
	if limit <= 0 || limit > 250 {
		limit = 50
	}
	rows, err := r.db.Query(ctx,
		`WITH recent AS (
		   SELECT pe.flow, pe.ref
		   FROM payment_events pe
		   JOIN users u ON u.id = pe.user_id
		   WHERE u.public_id = $1
		   GROUP BY pe.flow, pe.ref
		   ORDER BY MAX(pe.at) DESC
		   LIMIT $2
		 )
		 SELECT pe.flow, pe.ref, pe.stage, pe.status, pe.detail, pe.metadata,
		        pe.first_at, pe.at, pe.attempts
		 FROM payment_events pe
		 JOIN users u ON u.id = pe.user_id
		 JOIN recent rc ON rc.flow = pe.flow AND rc.ref = pe.ref
		 WHERE u.public_id = $1
		 ORDER BY pe.at DESC, pe.first_at ASC`,
		userPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func (r *PaymentTraceRepo) ByRef(ctx context.Context, userPublicID, flow, ref string) ([]paymenttrace.Event, error) {
	rows, err := r.db.Query(ctx,
		`SELECT pe.flow, pe.ref, pe.stage, pe.status, pe.detail, pe.metadata,
		        pe.first_at, pe.at, pe.attempts
		 FROM payment_events pe
		 JOIN users u ON u.id = pe.user_id
		 WHERE u.public_id = $1 AND pe.flow = $2 AND pe.ref = $3
		 ORDER BY pe.first_at ASC`,
		userPublicID, flow, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// RecentFailures is the operator's triage list: what is broken right now, across
// everyone, newest first.
func (r *PaymentTraceRepo) RecentFailures(ctx context.Context, limit int) ([]paymenttrace.FailureRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.Query(ctx,
		`SELECT u.public_id, pe.flow, pe.ref, pe.stage, pe.detail, pe.at
		 FROM payment_events pe
		 JOIN users u ON u.id = pe.user_id
		 WHERE pe.status = 'failed'
		 ORDER BY pe.at DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []paymenttrace.FailureRow{}
	for rows.Next() {
		var f paymenttrace.FailureRow
		if err := rows.Scan(&f.UserPublicID, &f.Flow, &f.Ref, &f.Stage, &f.Detail, &f.At); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// rowScanner is the slice of pgx.Rows scanEvents needs.
type rowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanEvents(rows rowScanner) ([]paymenttrace.Event, error) {
	out := []paymenttrace.Event{}
	for rows.Next() {
		var e paymenttrace.Event
		var meta []byte
		if err := rows.Scan(&e.Flow, &e.Ref, &e.Stage, &e.Status, &e.Detail, &meta,
			&e.FirstAt, &e.At, &e.Attempts); err != nil {
			return nil, err
		}
		if len(meta) > 0 {
			// A malformed metadata blob must not lose the stage it belongs to — the
			// stage is the diagnosis, the metadata is decoration.
			_ = json.Unmarshal(meta, &e.Metadata)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MoneyTrace is the platform-wide attempt scoreboard. Failed counts are from
// payment_events (the only place a mid-flow break is recorded). Completed
// deposits, payouts, and open holds come from the ledger tables — the log is
// never the authority on whether coins moved.
func (r *PaymentTraceRepo) MoneyTrace(ctx context.Context) (paymenttrace.MoneyTrace, error) {
	var s paymenttrace.MoneyTrace
	err := r.db.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM payment_events WHERE status = 'failed')::int,
		  (SELECT count(DISTINCT user_id) FROM payment_events WHERE status = 'failed')::int,
		  (SELECT count(*) FROM payment_events WHERE status = 'failed' AND flow = 'deposit')::int,
		  (SELECT count(*) FROM payment_events WHERE status = 'failed' AND flow = 'withdrawal')::int,
		  (SELECT count(*) FROM payment_events WHERE status = 'failed' AND flow = 'topup')::int,
		  (SELECT count(*) FROM ledger_transactions WHERE kind = 'topup')::bigint,
		  (SELECT count(*) FROM withdrawals WHERE status = 'paid')::bigint,
		  (SELECT count(*) FROM withdrawals WHERE status IN ('rejected', 'failed'))::bigint,
		  (SELECT count(*) FROM withdrawals WHERE status IN ('requested', 'approved', 'processing', 'broadcasted'))::bigint,
		  (SELECT count(*) FROM payout_holds WHERE status = 'held')::bigint
	`).Scan(
		&s.FailedAttempts, &s.UniqueUsersFailed,
		&s.DepositFailures, &s.WithdrawalFailures, &s.TopupFailures,
		&s.DepositsCompleted, &s.WithdrawalsPaid, &s.WithdrawalsRejected,
		&s.WithdrawalsPending, &s.OpenPayoutHolds,
	)
	return s, err
}
