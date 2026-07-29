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

// Withdrawable = the agent's current wallet balance. The FULL balance is withdrawable
// anytime (deposited coins included, not just match winnings): the platform's margin is
// the fee taken on every deposit AND every withdrawal, so a deposit→withdraw round-trip
// costs ~10% (5% in + 5% out) — which, with the anti-fraud gate, KYC/verified-wallet
// checks, velocity caps and the new-address cooldown, deters the buy→cash-out laundering
// vector without locking deposits in play.
//
// No "committed withdrawals" subtraction: a withdrawal Request escrows the coins
// (bank.Hold agent→escrow) BEFORE the row exists, so the balance already excludes every
// in-flight and paid withdrawal. Subtracting them again would under-report. Concurrency
// is handled by the per-owner advisory lock + the ledger's non-negative constraint.
// Withdrawable is what an agent may actually cash out: the lesser of its balance and
// the owner's NET PLAY RESULT.
//
// This used to return the raw wallet balance, which made the platform a mixer. Deposit
// USDC, play nothing, withdraw to a different address, and dirty funds come out the
// other side looking like gaming winnings — for the cost of the withdrawal fee. That
// is the single fastest way for a money platform to lose its banking and exchange
// access, and it is the risk the comment on the owner-lock already named ("beyond
// their net winnings") while nothing actually enforced it.
//
// The rule is the one every regulated betting operator uses: DEPOSITS MUST BE PLAYED,
// not parked. Winnings from settled matches are withdrawable; money that only ever sat
// in the wallet is not — it can be spent on entry fees, which is what it is for.
//
// net = (winnings from finished matches) - (already withdrawn)
//
// Both sides are historical facts from settled rows, so this cannot be gamed by an
// in-flight match. Deposits are deliberately absent from the formula: they raise the
// BALANCE (so you can play) without raising the entitlement (so you cannot launder).
// A user who deposits and then wins can withdraw their winnings; the deposit itself
// stays as stake until it is played.
func (r *PayoutRepo) Withdrawable(ctx context.Context, agentPublicID string) (int64, error) {
	var balance, netPlay int64
	err := r.db.QueryRow(ctx,
		`WITH ag AS (SELECT a.id, a.owner_user_id FROM agents a WHERE a.public_id = $1)
		 SELECT
		   COALESCE((SELECT wl.balance FROM wallets wl WHERE wl.agent_id = (SELECT id FROM ag)), 0),
		   COALESCE((SELECT SUM(mp.coins_delta) FROM match_players mp
		               JOIN matches m ON m.id = mp.match_id
		               JOIN agents a2 ON a2.id = mp.agent_id
		              WHERE a2.owner_user_id = (SELECT owner_user_id FROM ag)
		                AND m.status = 'finished'), 0)
		   -
		   COALESCE((SELECT SUM(wd.coins) FROM withdrawals wd
		              WHERE wd.user_id = (SELECT owner_user_id FROM ag)
		                AND wd.status IN ('paid','approved','requested')), 0)`,
		agentPublicID).Scan(&balance, &netPlay)
	if err != nil {
		return 0, err
	}
	// A losing player has a negative net result; they are owed nothing, not a negative.
	if netPlay < 0 {
		netPlay = 0
	}
	// Never more than is actually in the wallet, whatever the play history says.
	if netPlay < balance {
		return netPlay, nil
	}
	return balance, nil
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

func (r *PayoutRepo) PrimaryAgent(ctx context.Context, ownerUserPublicID string) (string, error) {
	var agent string
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE((
		     SELECT a.public_id
		     FROM agents a JOIN users u ON u.id = a.owner_user_id
		     WHERE u.public_id = $1
		     ORDER BY a.created_at
		     LIMIT 1
		   ), '')`,
		ownerUserPublicID).Scan(&agent)
	return agent, err
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

// OutstandingLiabilityCents sums the net cents of withdrawals the platform is on
// the hook to pay but hasn't yet settled (requested/processing/broadcasted). The
// solvency monitor compares this to the hot wallet's on-chain USDC balance.
func (r *PayoutRepo) OutstandingLiabilityCents(ctx context.Context) (int64, error) {
	var cents int64
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(net_cents), 0) FROM withdrawals
		 WHERE status IN ('requested','processing','broadcasted')`).Scan(&cents)
	return cents, err
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

// NetPaidBetween sums PLATFORM-WIDE net payout cents filed in a period — the input to
// the payout circuit breaker.
//
// Deliberately not per-owner: the breaker exists precisely to catch what per-owner
// limits cannot, which is many accounts each withdrawing a legal amount at once.
//
// Counts anything that has left or is committed to leaving ('requested' onward), not
// just 'paid'. A queue of approved-but-unsent payouts is money already gone as far as
// the treasury is concerned, and waiting for settlement to notice would mean the
// breaker trips after the drain rather than during it.
func (r *PayoutRepo) NetPaidBetween(ctx context.Context, from, to time.Time) (int64, error) {
	var cents int64
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(w.net_cents), 0) FROM withdrawals w
		  WHERE w.requested_at >= $1 AND w.requested_at < $2
		    AND w.status IN ('requested','approved','processing','broadcasted','paid')`,
		from, to).Scan(&cents)
	return cents, err
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

// WithOwnerLock serializes withdrawal requests for a single owner. It pins ONE
// pooled connection, takes a session-level Postgres advisory lock keyed on a
// 64-bit hash of the owner id, runs fn, then releases the lock and the connection.
// A concurrent request for the same owner blocks on pg_advisory_lock until the
// first releases — so the second one's Withdrawable/velocity reads see the first
// request's committed row and are correctly rejected (closes the over-withdraw /
// laundering / velocity-bypass race). Different owners hash to different keys and
// do not contend. The advisory lock is session-scoped (tied to the pinned
// connection), not transaction-scoped, so it holds across the multiple statements
// fn issues without forcing them into one transaction.
func (r *PayoutRepo) WithOwnerLock(ctx context.Context, ownerUserPublicID string, fn func() error) error {
	conn, err := r.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	// hashtext maps the id to int4; ::bigint widens it to the advisory-lock key type.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext($1)::bigint)`, ownerUserPublicID); err != nil {
		return err
	}
	defer func() {
		// Best-effort unlock; connection release also drops session locks, so a
		// failure here (e.g. ctx cancelled) does not leak the lock past checkin.
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext($1)::bigint)`, ownerUserPublicID)
	}()
	return fn()
}
