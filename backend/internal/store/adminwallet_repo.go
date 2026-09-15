package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/agent-arena/arena/internal/adminapi"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminWalletRepo serves the operator's per-user money view.
type AdminWalletRepo struct{ db *pgxpool.Pool }

func NewAdminWalletRepo(db *pgxpool.Pool) *AdminWalletRepo { return &AdminWalletRepo{db: db} }

var _ adminapi.WalletRepo = (*AdminWalletRepo)(nil)

// UserWallet returns every wallet attached to the account and where the coins sit.
func (r *AdminWalletRepo) UserWallet(ctx context.Context, userPublicID string) (adminapi.UserWalletDetail, bool, error) {
	var (
		d               adminapi.UserWalletDetail
		hintAddr        *string
		hintProvider    *string
		verifiedAddr    *string
		verifiedAt      *time.Time
		frozen          bool
		treasury        int64
		agentsBalance   int64
		lockedInMatches int64
		pendingPayouts  int64
	)

	err := r.db.QueryRow(ctx,
		`WITH u AS (SELECT id, public_id FROM users WHERE public_id = $1)
		 SELECT
		   (SELECT public_id FROM u),
		   (SELECT wallet_address FROM users WHERE id = (SELECT id FROM u)),
		   (SELECT wallet_provider FROM users WHERE id = (SELECT id FROM u)),
		   (SELECT verified_wallet_address FROM users WHERE id = (SELECT id FROM u)),
		   (SELECT wallet_verified_at FROM users WHERE id = (SELECT id FROM u)),
		   -- The Super Admin freeze flag lives on the treasury wallet row itself
		   -- (migration 0034), not in a separate table.
		   COALESCE((SELECT w.frozen FROM wallets w WHERE w.user_id = (SELECT id FROM u)), false),
		   -- The owner's own treasury.
		   COALESCE((SELECT w.balance FROM wallets w WHERE w.user_id = (SELECT id FROM u)), 0)::bigint,
		   -- Coins already pushed out to their agents. NOT part of the treasury.
		   COALESCE((SELECT SUM(w.balance) FROM wallets w
		               JOIN agents a ON a.id = w.agent_id
		              WHERE a.owner_user_id = (SELECT id FROM u)), 0)::bigint,
		   -- Staked into matches that have not settled.
		   COALESCE((SELECT SUM(m.bid) FROM match_players mp
		               JOIN matches m ON m.id = mp.match_id
		               JOIN agents a ON a.id = mp.agent_id
		              WHERE a.owner_user_id = (SELECT id FROM u)
		                AND m.status NOT IN ('finished', 'aborted')), 0)::bigint,
		   -- Committed to leaving but not yet gone.
		   COALESCE((SELECT SUM(wd.coins) FROM withdrawals wd
		              WHERE wd.user_id = (SELECT id FROM u)
		                AND wd.status IN ('requested', 'approved', 'processing', 'broadcasted')), 0)::bigint
		 WHERE EXISTS (SELECT 1 FROM u)`,
		userPublicID).
		Scan(&d.User, &hintAddr, &hintProvider, &verifiedAddr, &verifiedAt, &frozen,
			&treasury, &agentsBalance, &lockedInMatches, &pendingPayouts)
	if errors.Is(err, pgx.ErrNoRows) {
		return adminapi.UserWalletDetail{}, false, nil
	}
	if err != nil {
		return adminapi.UserWalletDetail{}, false, err
	}

	d.Frozen = frozen
	d.TreasuryBalance = treasury
	d.AgentsBalance = agentsBalance
	d.LockedInMatches = lockedInMatches
	d.PendingPayouts = pendingPayouts
	// Locked and pending are already inside the agent balances, so summing all four
	// would double-count. The total is what the account controls, once.
	d.Total = treasury + agentsBalance

	// Both addresses are listed, separately labelled. The login hint is what the
	// auth provider reported and is UNVERIFIED; only the signed address can be paid.
	// An operator who cannot see the difference will eventually promise a payout to
	// an address that can never receive one.
	if verifiedAddr != nil && *verifiedAddr != "" {
		cw := adminapi.ConnectedWallet{
			Address: *verifiedAddr, Role: "payout_destination", Verified: true,
		}
		if hintProvider != nil {
			cw.Provider = *hintProvider
		}
		if verifiedAt != nil {
			t := *verifiedAt
			cw.VerifiedAt = &t
		}
		d.Wallets = append(d.Wallets, cw)
	}
	if hintAddr != nil && *hintAddr != "" &&
		(verifiedAddr == nil || *hintAddr != *verifiedAddr) {
		cw := adminapi.ConnectedWallet{Address: *hintAddr, Role: "login_hint"}
		if hintProvider != nil {
			cw.Provider = *hintProvider
		}
		d.Wallets = append(d.Wallets, cw)
	}
	if d.Wallets == nil {
		d.Wallets = []adminapi.ConnectedWallet{}
	}

	hist, err := r.walletHistory(ctx, userPublicID)
	if err != nil {
		return adminapi.UserWalletDetail{}, false, err
	}
	d.WalletHistory = hist

	agents, err := r.userAgents(ctx, userPublicID)
	if err != nil {
		return adminapi.UserWalletDetail{}, false, err
	}
	d.Agents = agents
	return d, true, nil
}

// walletHistory reads every wallet this account has attached or removed, newest first.
//
// This is the half of the soft delete an operator can actually see. The users columns go
// NULL on removal — that is what makes the wallet gone for the developer — so without
// this read the admin panel would show the same emptiness the developer does, and the
// audit trail would sit in a table nothing queries.
//
// Capped at 50. An account with more wallet churn than that has a problem the newest 50
// rows will already show, and an unbounded read behind a support screen is how one
// pathological account makes the panel time out for everybody.
func (r *AdminWalletRepo) walletHistory(ctx context.Context, userPublicID string) ([]adminapi.WalletHistoryEntry, error) {
	rows, err := r.db.Query(ctx,
		`SELECT h.action, COALESCE(h.verified_address, ''), COALESCE(h.wallet_address, ''),
		        COALESCE(h.provider, ''), h.actor, h.created_at
		   FROM user_wallet_history h
		   JOIN users u ON u.id = h.user_id
		  WHERE u.public_id = $1
		  ORDER BY h.created_at DESC
		  LIMIT 50`, userPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []adminapi.WalletHistoryEntry{}
	for rows.Next() {
		var e adminapi.WalletHistoryEntry
		var verified, hint string
		if err := rows.Scan(&e.Action, &verified, &hint, &e.Provider, &e.Actor, &e.At); err != nil {
			return nil, err
		}
		// The proven address wins. Falling back to the hint means a row is never blank,
		// and Verified is what tells the operator which one they are looking at — a hint
		// shown as though it were a payout destination is how somebody gets told to
		// expect money at an address that could never receive it.
		if verified != "" {
			e.Address, e.Verified = verified, true
		} else {
			e.Address = hint
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *AdminWalletRepo) userAgents(ctx context.Context, userPublicID string) ([]adminapi.AgentBalance, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id,
		        COALESCE(a.name, ''),
		        COALESCE(w.balance, 0)::bigint,
		        COALESCE((SELECT SUM(m.bid) FROM match_players mp
		                    JOIN matches m ON m.id = mp.match_id
		                   WHERE mp.agent_id = a.id
		                     AND m.status NOT IN ('finished', 'aborted')), 0)::bigint,
		        COALESCE((SELECT SUM(wd.coins) FROM withdrawals wd
		                   WHERE wd.agent_id = a.id
		                     AND wd.status IN ('requested','approved','processing','broadcasted')), 0)::bigint,
		        COALESCE((SELECT count(*) FROM match_players mp
		                    JOIN matches m ON m.id = mp.match_id
		                   WHERE mp.agent_id = a.id
		                     AND m.status NOT IN ('finished', 'aborted')), 0)::int
		 FROM agents a
		 JOIN users u ON u.id = a.owner_user_id
		 LEFT JOIN wallets w ON w.agent_id = a.id
		 WHERE u.public_id = $1 AND a.kind <> 'house'
		 ORDER BY w.balance DESC NULLS LAST, a.created_at ASC`,
		userPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []adminapi.AgentBalance{}
	for rows.Next() {
		var a adminapi.AgentBalance
		if err := rows.Scan(&a.Agent, &a.Name, &a.Balance, &a.LockedInMatches,
			&a.PendingWithdrawn, &a.ActiveMatches); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UserTransactions returns the ledger lines against the user's OWN treasury
// wallet, newest first.
//
// Treasury only, not the agents' wallets. An operator investigating a deposit or
// a withdrawal is asking about the owner's pot; interleaving every stake and
// settlement from every agent would bury the handful of lines they came for under
// hundreds of match rows. Agent balances are shown separately in UserWallet.
func (r *AdminWalletRepo) UserTransactions(ctx context.Context, userPublicID string, limit, offset int) ([]adminapi.LedgerLine, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.db.Query(ctx,
		`SELECT t.public_id, t.kind, e.amount,
		        COALESCE(t.metadata, '{}'::jsonb), t.created_at
		 FROM ledger_entries e
		 JOIN ledger_transactions t ON t.id = e.txn_id
		 JOIN wallets w ON w.id = e.wallet_id
		 JOIN users u ON u.id = w.user_id
		 WHERE u.public_id = $1
		 ORDER BY t.created_at DESC, e.id DESC
		 LIMIT $2 OFFSET $3`,
		userPublicID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []adminapi.LedgerLine{}
	for rows.Next() {
		var l adminapi.LedgerLine
		var meta []byte
		if err := rows.Scan(&l.TxnID, &l.Kind, &l.Amount, &meta, &l.CreatedAt); err != nil {
			return nil, err
		}
		if len(meta) > 0 {
			// Metadata is context, the amount is the fact. A malformed blob must not
			// drop the line it belongs to.
			_ = json.Unmarshal(meta, &l.Metadata)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
