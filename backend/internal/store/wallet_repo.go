package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/wallet"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WalletRepo is the pgx implementation of wallet.Repo: the read-side facts the
// limit engine and wallet view need, all derived from already-persisted
// match/agent state. It performs no writes (coin moves go through LedgerRepo).
type WalletRepo struct{ db *pgxpool.Pool }

func NewWalletRepo(db *pgxpool.Pool) *WalletRepo { return &WalletRepo{db: db} }

var _ wallet.Repo = (*WalletRepo)(nil)

func (r *WalletRepo) Settlement(ctx context.Context, matchPublicID string) (wallet.Settlement, error) {
	rows, err := r.db.Query(ctx,
		`SELECT m.bid, m.rake_pct, COALESCE(wa.public_id, ''), ag.public_id
		 FROM match_players mp
		 JOIN matches m ON m.id = mp.match_id
		 JOIN agents  ag ON ag.id = mp.agent_id
		 LEFT JOIN agents wa ON wa.id = m.winner_agent_id
		 WHERE m.public_id = $1
		 ORDER BY mp.seat`, matchPublicID)
	if err != nil {
		return wallet.Settlement{}, err
	}
	defer rows.Close()
	var out wallet.Settlement
	for rows.Next() {
		var bid int64
		var rakePct int
		var winner, agent string
		if err := rows.Scan(&bid, &rakePct, &winner, &agent); err != nil {
			return wallet.Settlement{}, err
		}
		out.Bid, out.RakePct, out.Winner = bid, rakePct, winner
		out.Agents = append(out.Agents, agent)
	}
	if err := rows.Err(); err != nil {
		return wallet.Settlement{}, err
	}
	if len(out.Agents) == 0 {
		return wallet.Settlement{}, httpx.ErrNotFound
	}
	return out, nil
}

// SaveHeldSettlement upserts the computed multi-winner payout split for a match
// held for review, so an admin release replays it exactly (idempotent).
func (r *WalletRepo) SaveHeldSettlement(ctx context.Context, matchPublicID string, platformFee int64, payouts map[string]int64) error {
	if payouts == nil {
		payouts = map[string]int64{}
	}
	blob, err := json.Marshal(payouts)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx,
		`INSERT INTO held_settlements (match_public_id, platform_fee, payouts)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (match_public_id) DO UPDATE
		   SET platform_fee = EXCLUDED.platform_fee, payouts = EXCLUDED.payouts`,
		matchPublicID, platformFee, blob)
	return err
}

// HeldSettlement returns a persisted held split, or found=false if none.
func (r *WalletRepo) HeldSettlement(ctx context.Context, matchPublicID string) (int64, map[string]int64, bool, error) {
	var platformFee int64
	var blob []byte
	err := r.db.QueryRow(ctx,
		`SELECT platform_fee, payouts FROM held_settlements WHERE match_public_id = $1`,
		matchPublicID).Scan(&platformFee, &blob)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	payouts := map[string]int64{}
	if len(blob) > 0 {
		if err := json.Unmarshal(blob, &payouts); err != nil {
			return 0, nil, false, err
		}
	}
	return platformFee, payouts, true, nil
}

func (r *WalletRepo) AgentLimits(ctx context.Context, agentPublicID string) (wallet.AgentLimits, error) {
	var l wallet.AgentLimits
	err := r.db.QueryRow(ctx,
		`SELECT coin_limit_per_match, daily_loss_limit, session_loss_limit, min_wallet_balance,
		        max_concurrent_matches, cooldown_losses, cooldown_seconds, max_bid
		 FROM agents WHERE public_id = $1`, agentPublicID).
		Scan(&l.CoinLimitPerMatch, &l.DailyLossLimit, &l.SessionLossLimit, &l.MinWalletBalance,
			&l.MaxConcurrentMatches, &l.CooldownLosses, &l.CooldownSeconds, &l.MaxBid)
	if errors.Is(err, pgx.ErrNoRows) {
		return wallet.AgentLimits{}, httpx.ErrNotFound
	}
	return l, err
}

// LossSince sums coins lost (a non-negative result) in matches finished at/after
// `since`. A loss is a player row with a negative coins_delta on a finished match.
func (r *WalletRepo) LossSince(ctx context.Context, agentPublicID string, since time.Time) (int64, error) {
	var loss int64
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(-SUM(mp.coins_delta), 0)
		 FROM match_players mp
		 JOIN matches m ON m.id = mp.match_id
		 JOIN agents  ag ON ag.id = mp.agent_id
		 WHERE ag.public_id = $1 AND m.status = 'finished'
		   AND m.finished_at >= $2 AND mp.coins_delta < 0`,
		agentPublicID, since).Scan(&loss)
	return loss, err
}

// NetSince sums NET coin change (wins − losses; can be negative) in matches
// finished at/after `since`. Powers the auto-play take-profit stop.
func (r *WalletRepo) NetSince(ctx context.Context, agentPublicID string, since time.Time) (int64, error) {
	var net int64
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(mp.coins_delta), 0)
		 FROM match_players mp
		 JOIN matches m ON m.id = mp.match_id
		 JOIN agents  ag ON ag.id = mp.agent_id
		 WHERE ag.public_id = $1 AND m.status = 'finished'
		   AND m.finished_at >= $2`,
		agentPublicID, since).Scan(&net)
	return net, err
}

func (r *WalletRepo) LossCountSince(ctx context.Context, agentPublicID string, since time.Time) (int, error) {
	var n int
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*)
		 FROM match_players mp
		 JOIN matches m ON m.id = mp.match_id
		 JOIN agents  ag ON ag.id = mp.agent_id
		 WHERE ag.public_id = $1 AND m.status = 'finished'
		   AND m.finished_at >= $2 AND mp.coins_delta < 0`,
		agentPublicID, since).Scan(&n)
	return n, err
}

func (r *WalletRepo) ActiveMatchCount(ctx context.Context, agentPublicID string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*)
		 FROM match_players mp
		 JOIN matches m ON m.id = mp.match_id
		 JOIN agents  ag ON ag.id = mp.agent_id
		 WHERE ag.public_id = $1 AND m.status = 'active'`,
		agentPublicID).Scan(&n)
	return n, err
}

func (r *WalletRepo) OwnerOf(ctx context.Context, agentPublicID string) (string, error) {
	var owner string
	err := r.db.QueryRow(ctx,
		`SELECT u.public_id FROM agents a JOIN users u ON u.id = a.owner_user_id WHERE a.public_id = $1`,
		agentPublicID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", httpx.ErrNotFound
	}
	return owner, err
}

func (r *WalletRepo) OutstandingDebt(ctx context.Context, agentPublicID string) (int64, error) {
	var debt int64
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE((SELECT d.outstanding_coins FROM debts d
		   JOIN agents a ON a.id = d.agent_id WHERE a.public_id = $1), 0)`,
		agentPublicID).Scan(&debt)
	return debt, err
}

func (r *WalletRepo) RecordDebt(ctx context.Context, agentPublicID string, coins int64) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO debts (agent_id, outstanding_coins, total_charged_back)
		 SELECT a.id, $2, $2 FROM agents a WHERE a.public_id = $1
		 ON CONFLICT (agent_id) DO UPDATE
		   SET outstanding_coins = debts.outstanding_coins + $2,
		       total_charged_back = debts.total_charged_back + $2,
		       updated_at = now()`,
		agentPublicID, coins)
	return err
}

func (r *WalletRepo) RepayDebt(ctx context.Context, agentPublicID string, coins int64) error {
	_, err := r.db.Exec(ctx,
		`UPDATE debts SET outstanding_coins = GREATEST(0, outstanding_coins - $2), updated_at = now()
		 WHERE agent_id = (SELECT id FROM agents WHERE public_id = $1)`,
		agentPublicID, coins)
	return err
}

func (r *WalletRepo) UserLifetimeStats(ctx context.Context, userPublicID string) (wallet.LifetimeStats, error) {
	var s wallet.LifetimeStats
	err := r.db.QueryRow(ctx,
		`WITH uw AS (SELECT w.id FROM wallets w JOIN users u ON u.id = w.user_id WHERE u.public_id = $1)
		 SELECT
		   COALESCE((SELECT SUM(e.amount) FROM ledger_entries e JOIN ledger_transactions t ON t.id = e.txn_id
		             WHERE e.wallet_id = (SELECT id FROM uw) AND t.kind = 'topup' AND e.amount > 0), 0),
		   COALESCE((SELECT SUM(wd.coins) FROM withdrawals wd JOIN users u ON u.id = wd.user_id
		             WHERE u.public_id = $1 AND wd.status = 'paid'), 0),
		   COALESCE((SELECT SUM(mp.coins_delta) FROM match_players mp
		             JOIN matches m ON m.id = mp.match_id JOIN agents a ON a.id = mp.agent_id
		             JOIN users u ON u.id = a.owner_user_id
		             WHERE u.public_id = $1 AND m.status = 'finished' AND mp.coins_delta > 0), 0)`,
		userPublicID).Scan(&s.Deposits, &s.Withdrawals, &s.Winnings)
	return s, err
}

func (r *WalletRepo) LinkedWallet(ctx context.Context, userPublicID string) (string, error) {
	var address string
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(verified_wallet_address, '') FROM users WHERE public_id = $1`,
		userPublicID).Scan(&address)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", httpx.ErrNotFound
	}
	return address, err
}

func (r *WalletRepo) OwnerAgents(ctx context.Context, userPublicID string) ([]wallet.AgentRow, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id, a.name FROM agents a
		 JOIN users u ON u.id = a.owner_user_id WHERE u.public_id = $1 ORDER BY a.created_at`,
		userPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wallet.AgentRow
	for rows.Next() {
		var row wallet.AgentRow
		if err := rows.Scan(&row.PublicID, &row.Name); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *WalletRepo) StakedInActiveMatches(ctx context.Context, agentPublicID string) (int64, error) {
	var staked int64
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(m.bid), 0)
		 FROM match_players mp JOIN matches m ON m.id = mp.match_id
		 JOIN agents a ON a.id = mp.agent_id
		 WHERE a.public_id = $1 AND m.status = 'active'`, agentPublicID).Scan(&staked)
	return staked, err
}

func (r *WalletRepo) PendingWithdrawalCoins(ctx context.Context, agentPublicID string) (int64, error) {
	var pending int64
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(w.coins), 0)
		 FROM withdrawals w JOIN agents a ON a.id = w.agent_id
		 WHERE a.public_id = $1 AND w.status = 'requested'`, agentPublicID).Scan(&pending)
	return pending, err
}

// WithdrawableCoins is the wallet-summary (UI) view of what an agent can cash out. It
// MUST match payout.Repo.Withdrawable (store/payout_repo.go): the current wallet balance
// (full balance withdrawable anytime — deposited coins included). No committed
// subtraction: a withdrawal Request escrows the coins before the row exists, so the
// balance already excludes in-flight/paid withdrawals.
func (r *WalletRepo) WithdrawableCoins(ctx context.Context, agentPublicID string) (int64, error) {
	var avail int64
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE((SELECT wl.balance FROM wallets wl
		   JOIN agents a ON a.id = wl.agent_id WHERE a.public_id = $1), 0)`,
		agentPublicID).Scan(&avail)
	return avail, err
}
