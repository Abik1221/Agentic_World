package store

import (
	"context"
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
