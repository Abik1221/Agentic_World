package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/agent-arena/arena/internal/events"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/payout"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PayoutRepo is the pgx implementation of payout.Repo.
type PayoutRepo struct{ db *pgxpool.Pool }

func NewPayoutRepo(db *pgxpool.Pool) *PayoutRepo { return &PayoutRepo{db: db} }

var _ payout.Repo = (*PayoutRepo)(nil)

// Withdrawable = net match winnings − coins already committed to live withdrawals,
// floored at 0 and capped at the current wallet balance. Deposited/bonus coins
// (which never appear in match coins_delta) are therefore NOT withdrawable.
func (r *PayoutRepo) Withdrawable(ctx context.Context, agentPublicID string) (int64, error) {
	var avail int64
	err := r.db.QueryRow(ctx,
		`WITH winnings AS (
		   SELECT COALESCE(SUM(mp.coins_delta), 0) AS net
		   FROM match_players mp
		   JOIN matches m ON m.id = mp.match_id
		   JOIN agents  a ON a.id = mp.agent_id
		   WHERE a.public_id = $1 AND m.status = 'finished'
		 ),
		 committed AS (
		   SELECT COALESCE(SUM(w.coins), 0) AS c
		   FROM withdrawals w JOIN agents a ON a.id = w.agent_id
		   WHERE a.public_id = $1 AND w.status IN ('requested','approved','processing','broadcasted','paid')
		 ),
		 bal AS (
		   SELECT COALESCE(wl.balance, 0) AS b
		   FROM wallets wl JOIN agents a ON a.id = wl.agent_id WHERE a.public_id = $1
		 )
		 SELECT GREATEST(0, LEAST((SELECT net FROM winnings) - (SELECT c FROM committed), (SELECT b FROM bal)))`,
		agentPublicID).Scan(&avail)
	return avail, err
}

func (r *PayoutRepo) AgentOwner(ctx context.Context, agentPublicID string) (string, string, error) {
	var owner, connect string
	err := r.db.QueryRow(ctx,
		`SELECT u.public_id, COALESCE(u.stripe_connect_id, '')
		 FROM agents a JOIN users u ON u.id = a.owner_user_id
		 WHERE a.public_id = $1`, agentPublicID).Scan(&owner, &connect)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", httpx.ErrNotFound
	}
	return owner, connect, err
}

func (r *PayoutRepo) DestinationWallet(ctx context.Context, ownerUserPublicID string) (string, error) {
	var wallet string
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(wallet_address, '') FROM users WHERE public_id = $1`, ownerUserPublicID).Scan(&wallet)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", httpx.ErrNotFound
	}
	return wallet, err
}

func (r *PayoutRepo) VerifiedWallet(ctx context.Context, ownerUserPublicID string) (string, error) {
	var wallet string
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(verified_wallet_address, '') FROM users WHERE public_id = $1`, ownerUserPublicID).Scan(&wallet)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", httpx.ErrNotFound
	}
	return wallet, err
}

func (r *PayoutRepo) VerifiedWalletAt(ctx context.Context, ownerUserPublicID string) (string, time.Time, error) {
	var wallet string
	var at *time.Time
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(verified_wallet_address, ''), wallet_verified_at
		 FROM users WHERE public_id = $1`, ownerUserPublicID).Scan(&wallet, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, httpx.ErrNotFound
	}
	if err != nil {
		return "", time.Time{}, err
	}
	if at == nil {
		return wallet, time.Time{}, nil
	}
	return wallet, *at, nil
}

// WithdrawnSince sums an owner's non-failed withdrawals filed since `since`.
func (r *PayoutRepo) WithdrawnSince(ctx context.Context, ownerUserPublicID string, since time.Time) (int, int64, error) {
	var count int
	var cents int64
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(SUM(w.net_cents), 0)
		 FROM withdrawals w JOIN users u ON u.id = w.user_id
		 WHERE u.public_id = $1 AND w.requested_at >= $2
		   AND w.status IN ('requested','processing','broadcasted','paid')`,
		ownerUserPublicID, since).Scan(&count, &cents)
	if err != nil {
		return 0, 0, err
	}
	return count, cents, nil
}

func (r *PayoutRepo) AgentFlagged(ctx context.Context, agentPublicID string) (bool, error) {
	var flagged bool
	err := r.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM fraud_flags f JOIN agents a ON a.id = f.agent_id
		                WHERE a.public_id = $1 AND f.active)`, agentPublicID).Scan(&flagged)
	return flagged, err
}

func (r *PayoutRepo) OutstandingDebt(ctx context.Context, agentPublicID string) (int64, error) {
	var debt int64
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE((SELECT d.outstanding_coins FROM debts d
		   JOIN agents a ON a.id = d.agent_id WHERE a.public_id = $1), 0)`,
		agentPublicID).Scan(&debt)
	return debt, err
}

func (r *PayoutRepo) Create(ctx context.Context, w payout.Withdrawal) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	chain := w.Chain
	if chain == "" {
		chain = "stripe"
	}
	ct, err := tx.Exec(ctx,
		`INSERT INTO withdrawals
		   (public_id, agent_id, user_id, coins, fee_coins, gross_cents, stripe_fee_cents, net_cents,
		    connect_account_id, chain, dest_wallet_address, status)
		 SELECT $1, a.id, u.id, $4, $5, $6, $7, $8, $9, $10, $11, 'requested'
		 FROM agents a JOIN users u ON u.id = a.owner_user_id
		 WHERE a.public_id = $2 AND u.public_id = $3`,
		w.PublicID, w.Agent, w.Owner, w.Coins, w.FeeCoins, w.GrossCents, w.StripeFeeCents, w.NetCents,
		nullString(w.ConnectAccount), chain, nullString(w.DestWallet))
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return httpx.ErrNotFound
	}
	// withdrawal.requested for the Super Admin mirror, in the same tx as the insert.
	payload, err := json.Marshal(map[string]any{
		"withdrawal_id": w.PublicID, "agent": w.Agent, "coins": w.Coins,
	})
	if err != nil {
		return err
	}
	if _, err := InsertEventTx(ctx, tx, events.TypeWithdrawalRequested, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PayoutRepo) Get(ctx context.Context, publicID string) (payout.Withdrawal, error) {
	var w payout.Withdrawal
	err := r.db.QueryRow(ctx,
		`SELECT w.public_id, ag.public_id, u.public_id, w.coins, w.fee_coins, w.gross_cents,
		        w.stripe_fee_cents, w.net_cents, COALESCE(w.connect_account_id, ''),
		        COALESCE(w.chain, 'stripe'), COALESCE(w.dest_wallet_address, ''), w.status,
		        COALESCE(w.transfer_id, ''), w.requested_at
		 FROM withdrawals w
		 JOIN agents ag ON ag.id = w.agent_id
		 JOIN users  u  ON u.id  = w.user_id
		 WHERE w.public_id = $1`, publicID).
		Scan(&w.PublicID, &w.Agent, &w.Owner, &w.Coins, &w.FeeCoins, &w.GrossCents,
			&w.StripeFeeCents, &w.NetCents, &w.ConnectAccount, &w.Chain, &w.DestWallet, &w.Status, &w.TransferID, &w.RequestedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return payout.Withdrawal{}, httpx.ErrNotFound
	}
	return w, err
}

func (r *PayoutRepo) GetByTransferID(ctx context.Context, transferID string) (payout.Withdrawal, error) {
	var w payout.Withdrawal
	err := r.db.QueryRow(ctx,
		`SELECT w.public_id, ag.public_id, u.public_id, w.coins, w.fee_coins, w.gross_cents,
		        w.stripe_fee_cents, w.net_cents, COALESCE(w.connect_account_id, ''), w.status,
		        COALESCE(w.transfer_id, ''), w.requested_at
		 FROM withdrawals w
		 JOIN agents ag ON ag.id = w.agent_id
		 JOIN users  u  ON u.id  = w.user_id
		 WHERE w.transfer_id = $1`, transferID).
		Scan(&w.PublicID, &w.Agent, &w.Owner, &w.Coins, &w.FeeCoins, &w.GrossCents,
			&w.StripeFeeCents, &w.NetCents, &w.ConnectAccount, &w.Status, &w.TransferID, &w.RequestedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return payout.Withdrawal{}, httpx.ErrNotFound
	}
	return w, err
}

func (r *PayoutRepo) SetStatus(ctx context.Context, publicID, from, to, transferID, reason string) (bool, error) {
	ct, err := r.db.Exec(ctx,
		`UPDATE withdrawals
		 SET status = $3,
		     transfer_id = COALESCE(NULLIF($4, ''), transfer_id),
		     reason = COALESCE(NULLIF($5, ''), reason),
		     resolved_at = now()
		 WHERE public_id = $1 AND status = $2`,
		publicID, from, to, transferID, reason)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}

func (r *PayoutRepo) PendingByConnectAccount(ctx context.Context, connectAccountID string) ([]payout.Withdrawal, error) {
	rows, err := r.db.Query(ctx,
		`SELECT w.public_id, ag.public_id, u.public_id, w.coins, w.fee_coins, w.gross_cents,
		        w.stripe_fee_cents, w.net_cents, COALESCE(w.connect_account_id, ''), w.status,
		        COALESCE(w.transfer_id, ''), w.requested_at
		 FROM withdrawals w
		 JOIN agents ag ON ag.id = w.agent_id
		 JOIN users  u  ON u.id  = w.user_id
		 WHERE w.connect_account_id = $1 AND w.status = 'requested'
		 ORDER BY w.requested_at ASC`, connectAccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []payout.Withdrawal
	for rows.Next() {
		var w payout.Withdrawal
		if err := rows.Scan(&w.PublicID, &w.Agent, &w.Owner, &w.Coins, &w.FeeCoins, &w.GrossCents,
			&w.StripeFeeCents, &w.NetCents, &w.ConnectAccount, &w.Status, &w.TransferID, &w.RequestedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (r *PayoutRepo) Audit(ctx context.Context, actor, action, target string, detail []byte) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO audit_log (actor, action, target, detail) VALUES ($1, $2, $3, $4::jsonb)`,
		actor, action, nullString(target), string(detail))
	return err
}

func (r *PayoutRepo) ListByOwner(ctx context.Context, ownerUserPublicID string, limit int) ([]payout.Withdrawal, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.db.Query(ctx,
		`SELECT w.public_id, ag.public_id, w.coins, w.fee_coins, w.gross_cents,
		        w.stripe_fee_cents, w.net_cents, COALESCE(w.chain, 'stripe'),
		        COALESCE(w.dest_wallet_address, ''), w.status, COALESCE(w.transfer_id, ''), w.requested_at
		 FROM withdrawals w
		 JOIN agents ag ON ag.id = w.agent_id
		 JOIN users  u  ON u.id  = w.user_id
		 WHERE u.public_id = $1
		 ORDER BY w.requested_at DESC LIMIT $2`, ownerUserPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []payout.Withdrawal
	for rows.Next() {
		var w payout.Withdrawal
		if err := rows.Scan(&w.PublicID, &w.Agent, &w.Coins, &w.FeeCoins, &w.GrossCents,
			&w.StripeFeeCents, &w.NetCents, &w.Chain, &w.DestWallet, &w.Status, &w.TransferID, &w.RequestedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (r *PayoutRepo) ListByStatus(ctx context.Context, status string, limit int) ([]payout.Withdrawal, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.Query(ctx,
		`SELECT w.public_id, ag.public_id, u.public_id, w.coins, w.fee_coins, w.gross_cents,
		        w.stripe_fee_cents, w.net_cents, COALESCE(w.connect_account_id, ''),
		        COALESCE(w.chain, 'stripe'), COALESCE(w.dest_wallet_address, ''), w.status,
		        COALESCE(w.transfer_id, ''), w.requested_at, COALESCE(w.resolved_at, w.requested_at)
		 FROM withdrawals w
		 JOIN agents ag ON ag.id = w.agent_id
		 JOIN users  u  ON u.id  = w.user_id
		 WHERE w.status = $1
		 ORDER BY w.requested_at ASC LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []payout.Withdrawal
	for rows.Next() {
		var w payout.Withdrawal
		if err := rows.Scan(&w.PublicID, &w.Agent, &w.Owner, &w.Coins, &w.FeeCoins, &w.GrossCents,
			&w.StripeFeeCents, &w.NetCents, &w.ConnectAccount, &w.Chain, &w.DestWallet, &w.Status, &w.TransferID, &w.RequestedAt, &w.StatusChangedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
