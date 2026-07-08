package store

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/walletrecon"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WalletReconRepo is the read-only pgx implementation of walletrecon.Repo.
type WalletReconRepo struct{ db *pgxpool.Pool }

func NewWalletReconRepo(db *pgxpool.Pool) *WalletReconRepo { return &WalletReconRepo{db: db} }

var _ walletrecon.Repo = (*WalletReconRepo)(nil)

// DepositTotals returns count + Σcoins of credited on-chain deposits, plus how
// many have NO matching ledger top-up (keyed 'solana:'+tx_signature) — the
// orphan case that means money was recorded on-chain but never hit the ledger.
func (r *WalletReconRepo) DepositTotals(ctx context.Context) (count, coins, orphans int64, err error) {
	err = r.db.QueryRow(ctx,
		`SELECT
		   count(*),
		   COALESCE(SUM(d.coins), 0),
		   COALESCE(SUM(CASE WHEN NOT EXISTS (
		     SELECT 1 FROM ledger_transactions t WHERE t.idempotency_key = 'solana:' || d.tx_signature
		   ) THEN 1 ELSE 0 END), 0)
		 FROM solana_deposits d`).Scan(&count, &coins, &orphans)
	return count, coins, orphans, err
}

// LedgerSolanaCoins sums the positive user-wallet postings of solana top-ups —
// i.e. the coins actually credited to users via the deposit path. Should equal
// the sum of solana_deposits.coins.
func (r *WalletReconRepo) LedgerSolanaCoins(ctx context.Context) (int64, error) {
	var coins int64
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(e.amount), 0)
		 FROM ledger_entries e
		 JOIN ledger_transactions t ON t.id = e.txn_id
		 JOIN wallets w ON w.id = e.wallet_id
		 WHERE t.idempotency_key LIKE 'solana:%' AND w.user_id IS NOT NULL AND e.amount > 0`).Scan(&coins)
	return coins, err
}

// StuckWithdrawals counts solana withdrawals sitting in `status` past the cutoff.
func (r *WalletReconRepo) StuckWithdrawals(ctx context.Context, status string, olderThan time.Duration) (int64, error) {
	var n int64
	err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM withdrawals
		 WHERE chain = 'solana' AND status = $1
		   AND COALESCE(resolved_at, requested_at) < now() - ($2 * interval '1 second')`,
		status, olderThan.Seconds()).Scan(&n)
	return n, err
}
