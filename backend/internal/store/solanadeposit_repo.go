package store

import (
	"context"
	"errors"

	"github.com/agent-arena/arena/internal/solanadeposit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DepositRepo is the pgx-backed implementation of solanadeposit.Repo.
type DepositRepo struct{ db *pgxpool.Pool }

// NewDepositRepo wires the repo to the connection pool.
func NewDepositRepo(db *pgxpool.Pool) *DepositRepo { return &DepositRepo{db: db} }

var _ solanadeposit.Repo = (*DepositRepo)(nil)

// sessionColumns is the fixed select list scanned by scanSession.
const sessionColumns = `d.public_id, u.public_id, d.reference, d.asset, d.amount_expected,
	d.coins_expected, d.status, COALESCE(d.tx_signature,''), COALESCE(d.amount_received,0),
	COALESCE(d.coins_credited,0), d.created_at, d.expires_at`

func scanSession(row pgx.Row) (solanadeposit.Session, error) {
	var s solanadeposit.Session
	err := row.Scan(&s.PublicID, &s.UserPublicID, &s.Reference, &s.Asset, &s.AmountExpected,
		&s.CoinsExpected, &s.Status, &s.TxSignature, &s.AmountReceived, &s.CoinsCredited,
		&s.CreatedAt, &s.ExpiresAt)
	return s, err
}

func (r *DepositRepo) CreateSession(ctx context.Context, s solanadeposit.Session) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO deposit_sessions
		     (public_id, user_id, reference, asset, amount_expected, coins_expected, status, created_at, expires_at)
		 VALUES ($1, (SELECT id FROM users WHERE public_id = $2), $3, $4, $5, $6, $7, $8, $9)`,
		s.PublicID, s.UserPublicID, s.Reference, s.Asset, s.AmountExpected, s.CoinsExpected,
		s.Status, s.CreatedAt, s.ExpiresAt)
	return err
}

func (r *DepositRepo) GetSession(ctx context.Context, publicID string) (solanadeposit.Session, error) {
	row := r.db.QueryRow(ctx,
		`SELECT `+sessionColumns+` FROM deposit_sessions d JOIN users u ON u.id = d.user_id
		 WHERE d.public_id = $1`, publicID)
	s, err := scanSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return solanadeposit.Session{}, solanadeposit.ErrNotFound
	}
	return s, err
}

func (r *DepositRepo) GetSessionForUser(ctx context.Context, publicID, userPublicID string) (solanadeposit.Session, error) {
	row := r.db.QueryRow(ctx,
		`SELECT `+sessionColumns+` FROM deposit_sessions d JOIN users u ON u.id = d.user_id
		 WHERE d.public_id = $1 AND u.public_id = $2`, publicID, userPublicID)
	s, err := scanSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return solanadeposit.Session{}, solanadeposit.ErrNotFound
	}
	return s, err
}

func (r *DepositRepo) ListByUser(ctx context.Context, userPublicID string, limit int) ([]solanadeposit.Session, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+sessionColumns+` FROM deposit_sessions d JOIN users u ON u.id = d.user_id
		 WHERE u.public_id = $1 ORDER BY d.created_at DESC LIMIT $2`, userPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectSessions(rows)
}

func (r *DepositRepo) OpenSessions(ctx context.Context, limit int) ([]solanadeposit.Session, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+sessionColumns+` FROM deposit_sessions d JOIN users u ON u.id = d.user_id
		 WHERE d.status IN ('pending','detected') ORDER BY d.created_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectSessions(rows)
}

func collectSessions(rows pgx.Rows) ([]solanadeposit.Session, error) {
	var out []solanadeposit.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *DepositRepo) MarkDetected(ctx context.Context, publicID, txSignature string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE deposit_sessions
		 SET status = 'detected',
		     tx_signature = COALESCE(NULLIF($2,''), tx_signature),
		     updated_at = now()
		 WHERE public_id = $1 AND status = 'pending'`,
		publicID, txSignature)
	return err
}

func (r *DepositRepo) CompleteCredit(ctx context.Context, in solanadeposit.CreditRecord) (bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after commit

	// Idempotent insert keyed on the tx signature; RowsAffected==0 ⇒ already recorded.
	ct, err := tx.Exec(ctx,
		`INSERT INTO solana_deposits (tx_signature, user_id, session_id, mint, amount_base, coins, slot)
		 VALUES ($1,
		         (SELECT id FROM users WHERE public_id = $2),
		         (SELECT id FROM deposit_sessions WHERE public_id = $3),
		         $4, $5, $6, $7)
		 ON CONFLICT (tx_signature) DO NOTHING`,
		in.TxSignature, in.UserPublicID, in.SessionPublicID, in.Mint, in.AmountBase, in.Coins, in.Slot)
	if err != nil {
		return false, err
	}
	already := ct.RowsAffected() == 0

	// Flip the session to completed (idempotent — skips if already completed).
	if _, err := tx.Exec(ctx,
		`UPDATE deposit_sessions
		 SET status = 'completed', tx_signature = $2, amount_received = $3, coins_credited = $4, updated_at = now()
		 WHERE public_id = $1 AND status <> 'completed'`,
		in.SessionPublicID, in.TxSignature, in.AmountBase, in.Coins); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return already, nil
}

func (r *DepositRepo) ExpireSession(ctx context.Context, publicID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE deposit_sessions SET status = 'expired', updated_at = now()
		 WHERE public_id = $1 AND status IN ('pending','detected')`, publicID)
	return err
}
