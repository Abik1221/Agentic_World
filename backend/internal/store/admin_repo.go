package store

import (
	"context"
	"time"

	"errors"

	"github.com/agent-arena/arena/internal/adminapi"
	"github.com/agent-arena/arena/internal/ledger"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminRepo is the pgx implementation of adminapi.Repo: read-only, newest-first,
// bounded lists plus a single-round-trip overview. It never mutates; the Super
// Admin's writes go through the existing scoped handlers.
type AdminRepo struct{ db *pgxpool.Pool }

func NewAdminRepo(db *pgxpool.Pool) *AdminRepo { return &AdminRepo{db: db} }

var _ adminapi.Repo = (*AdminRepo)(nil)

func (r *AdminRepo) ListUsers(ctx context.Context, limit, offset int) ([]adminapi.User, error) {
	rows, err := r.db.Query(ctx,
		`SELECT u.public_id, COALESCE(u.x_handle, ''), COALESCE(u.email::text, ''), u.status, u.created_at,
		        (SELECT count(*) FROM agents a WHERE a.owner_user_id = u.id) AS agents
		 FROM users u
		 ORDER BY u.created_at DESC
		 LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []adminapi.User
	for rows.Next() {
		var u adminapi.User
		if err := rows.Scan(&u.PublicID, &u.XHandle, &u.Email, &u.Status, &u.CreatedAt, &u.Agents); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *AdminRepo) ListAgents(ctx context.Context, limit, offset int) ([]adminapi.Agent, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id, a.name, u.public_id, COALESCE(a.framework, ''), a.status,
		        a.verification_level, COALESCE(w.balance, 0), a.created_at
		 FROM agents a
		 JOIN users u ON u.id = a.owner_user_id
		 LEFT JOIN wallets w ON w.agent_id = a.id AND w.kind = 'agent'
		 ORDER BY a.created_at DESC
		 LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []adminapi.Agent
	for rows.Next() {
		var a adminapi.Agent
		if err := rows.Scan(&a.PublicID, &a.Name, &a.OwnerPublicID, &a.Framework, &a.Status,
			&a.VerificationLevel, &a.Balance, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *AdminRepo) ListMatches(ctx context.Context, status string, limit, offset int) ([]adminapi.Match, error) {
	rows, err := r.db.Query(ctx,
		`SELECT m.public_id, m.game, m.status, m.mode, m.bid, COALESCE(win.public_id, ''),
		        m.created_at, m.started_at, m.finished_at
		 FROM matches m
		 LEFT JOIN agents win ON win.id = m.winner_agent_id
		 WHERE ($1 = '' OR m.status = $1)
		 ORDER BY m.created_at DESC
		 LIMIT $2 OFFSET $3`, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []adminapi.Match
	for rows.Next() {
		var m adminapi.Match
		if err := rows.Scan(&m.PublicID, &m.Game, &m.Status, &m.Mode, &m.Bid, &m.WinnerAgent,
			&m.CreatedAt, &m.StartedAt, &m.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListPayments returns coin topups (ledger transactions of kind 'topup'), each
// annotated with the credited amount and, best-effort, the agent whose wallet
// received it.
func (r *AdminRepo) ListPayments(ctx context.Context, limit, offset int) ([]adminapi.Payment, error) {
	rows, err := r.db.Query(ctx,
		`SELECT t.public_id, t.kind,
		        COALESCE((SELECT SUM(e.amount) FROM ledger_entries e
		                  WHERE e.txn_id = t.id AND e.amount > 0), 0) AS amount,
		        COALESCE((SELECT a.public_id FROM ledger_entries e
		                  JOIN wallets w ON w.id = e.wallet_id
		                  JOIN agents  a ON a.id = w.agent_id
		                  WHERE e.txn_id = t.id AND w.kind = 'agent'
		                  LIMIT 1), '') AS agent,
		        t.created_at
		 FROM ledger_transactions t
		 WHERE t.kind = 'topup'
		 ORDER BY t.created_at DESC
		 LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []adminapi.Payment
	for rows.Next() {
		var p adminapi.Payment
		if err := rows.Scan(&p.PublicID, &p.Kind, &p.Amount, &p.Agent, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *AdminRepo) ListDisputes(ctx context.Context, status string, limit, offset int) ([]adminapi.Dispute, error) {
	rows, err := r.db.Query(ctx,
		`SELECT d.public_id, d.kind, d.status, COALESCE(m.public_id, ''), COALESCE(a.public_id, ''),
		        COALESCE(u.public_id, ''), COALESCE(d.detail, ''), COALESCE(d.resolution, ''),
		        d.created_at, d.resolved_at
		 FROM disputes d
		 LEFT JOIN matches m ON m.id = d.match_id
		 LEFT JOIN agents  a ON a.id = d.agent_id
		 LEFT JOIN users   u ON u.id = d.reporter_user_id
		 WHERE ($1 = '' OR d.status = $1)
		 ORDER BY d.created_at DESC
		 LIMIT $2 OFFSET $3`, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []adminapi.Dispute
	for rows.Next() {
		var d adminapi.Dispute
		if err := rows.Scan(&d.PublicID, &d.Kind, &d.Status, &d.Match, &d.Agent, &d.Reporter,
			&d.Detail, &d.Resolution, &d.CreatedAt, &d.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UserDetail assembles one developer's full operator record.
//
// Every figure is its own SCALAR SUBQUERY rather than a chain of JOINs. That is not
// style: joining agents -> matches -> withdrawals -> ledger in one pass multiplies
// rows (a user with 3 agents and 4 withdrawals produces 12 copies of each), and every
// SUM over it silently inflates. Money that is wrong in a plausible direction is worse
// than money that is obviously missing, so each number is computed in isolation where
// it cannot be multiplied by an unrelated table.
//
// Sandbox and competitive are counted separately throughout — a combined figure hides
// the exact patterns an operator is looking for.
func (r *AdminRepo) UserDetail(ctx context.Context, userPublicID string) (adminapi.UserDetail, bool, error) {
	var d adminapi.UserDetail
	err := r.db.QueryRow(ctx,
		`WITH u AS (SELECT id, public_id, username, email, status, created_at
		              FROM users WHERE public_id = $1)
		 SELECT
		   (SELECT public_id FROM u),
		   COALESCE((SELECT username::text FROM u), ''),
		   COALESCE((SELECT email::text FROM u), ''),
		   (SELECT status FROM u),
		   (SELECT created_at FROM u),
		   COALESCE((SELECT count(*) FROM agents a
		              WHERE a.owner_user_id = (SELECT id FROM u) AND a.kind <> 'house'), 0)::int,

		   -- Play, split by mode. COUNT(DISTINCT m.id) so a match with two of this
		   -- user's own agents seated counts once, not twice.
		   COALESCE((SELECT count(DISTINCT m.id) FROM matches m
		               JOIN match_players mp ON mp.match_id = m.id
		               JOIN agents a ON a.id = mp.agent_id
		              WHERE a.owner_user_id = (SELECT id FROM u) AND a.kind <> 'house'
		                AND m.status = 'finished' AND m.mode <> 'sandbox'), 0)::int,
		   COALESCE((SELECT count(DISTINCT m.id) FROM matches m
		               JOIN match_players mp ON mp.match_id = m.id
		               JOIN agents a ON a.id = mp.agent_id
		              WHERE a.owner_user_id = (SELECT id FROM u) AND a.kind <> 'house'
		                AND m.status = 'finished' AND m.mode = 'sandbox'), 0)::int,

		   -- Record from ratings, which sandbox never writes to.
		   COALESCE((SELECT SUM(rt.wins) FROM ratings rt JOIN agents a ON a.id = rt.agent_id
		              WHERE a.owner_user_id = (SELECT id FROM u) AND a.kind <> 'house'), 0)::int,
		   COALESCE((SELECT SUM(rt.losses) FROM ratings rt JOIN agents a ON a.id = rt.agent_id
		              WHERE a.owner_user_id = (SELECT id FROM u) AND a.kind <> 'house'), 0)::int,

		   -- Money.
		   COALESCE((SELECT SUM(w.balance) FROM wallets w JOIN agents a ON a.id = w.agent_id
		              WHERE a.owner_user_id = (SELECT id FROM u)), 0)::bigint,
		   COALESCE((SELECT SUM(e.amount) FROM ledger_entries e
		               JOIN ledger_transactions t ON t.id = e.txn_id
		               JOIN wallets w ON w.id = e.wallet_id
		              WHERE w.user_id = (SELECT id FROM u) AND t.kind = 'topup' AND e.amount > 0), 0)::bigint,
		   COALESCE((SELECT SUM(mp.coins_delta) FROM match_players mp
		               JOIN matches m ON m.id = mp.match_id
		               JOIN agents a ON a.id = mp.agent_id
		              WHERE a.owner_user_id = (SELECT id FROM u) AND a.kind <> 'house'
		                AND m.status = 'finished' AND mp.coins_delta > 0), 0)::bigint,
		   COALESCE((SELECT SUM(wd.coins) FROM withdrawals wd
		              WHERE wd.user_id = (SELECT id FROM u) AND wd.status = 'paid'), 0)::bigint,

		   -- What the platform earned from this user: rake on their settled matches
		   -- plus the fee on their withdrawals.
		   -- DISTINCT the matches BEFORE summing. Joining through match_players emits
		   -- one row per seat, so a user with two agents on the same table had that
		   -- match's rake counted twice — revenue inflated by a plausible-looking
		   -- amount, which is the worst way for a money figure to be wrong. The match
		   -- COUNTs above were already protected by COUNT(DISTINCT); this SUM was not.
		   COALESCE((SELECT SUM(dm.bid * dm.rake_pct / 100) FROM (
		               SELECT DISTINCT m.id, m.bid, m.rake_pct FROM matches m
		                 JOIN match_players mp ON mp.match_id = m.id
		                 JOIN agents a ON a.id = mp.agent_id
		                WHERE a.owner_user_id = (SELECT id FROM u) AND a.kind <> 'house'
		                  AND m.status = 'finished' AND m.bid > 0
		             ) dm), 0)::bigint
		   + COALESCE((SELECT SUM(wd.fee_coins) FROM withdrawals wd
		              WHERE wd.user_id = (SELECT id FROM u) AND wd.status = 'paid'), 0)::bigint,

		   -- Committed to leaving but not yet gone.
		   COALESCE((SELECT SUM(wd.coins) FROM withdrawals wd
		              WHERE wd.user_id = (SELECT id FROM u)
		                AND wd.status IN ('requested','approved','processing','broadcasted')), 0)::bigint,

		   COALESCE((SELECT SUM(b.tokens) FROM agent_match_benchmark b
		               JOIN agents a ON a.id = b.agent_id
		              WHERE a.owner_user_id = (SELECT id FROM u)), 0)::bigint
		`, userPublicID).Scan(
		&d.PublicID, &d.Username, &d.Email, &d.Status, &d.CreatedAt, &d.Agents,
		&d.CompetitiveMatches, &d.SandboxMatches, &d.Wins, &d.Losses,
		&d.Balance, &d.LifetimeDeposits, &d.LifetimeWinnings, &d.WithdrawnCoins,
		&d.PlatformRevenue, &d.PendingWithdrawals, &d.TokensUsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return adminapi.UserDetail{}, false, nil
	}
	if err != nil {
		return adminapi.UserDetail{}, false, err
	}
	return d, true, nil
}

// Overview computes the platform aggregate in one round trip. Platform revenue is
// the running balance of the platform_revenue system wallet (rake accrual).
func (r *AdminRepo) Overview(ctx context.Context) (adminapi.Overview, error) {
	var ov adminapi.Overview
	err := r.db.QueryRow(ctx,
		`SELECT
		   (SELECT count(*) FROM users),
		   (SELECT count(*) FROM agents),
		   (SELECT count(*) FROM matches WHERE status = 'active'),
		   (SELECT count(*) FROM matches WHERE status = 'finished'),
		   (SELECT count(*) FROM disputes WHERE status IN ('open', 'reviewing')),
		   (SELECT count(*) FROM withdrawals WHERE status IN ('requested', 'approved')),
		   (SELECT COALESCE(balance, 0) FROM wallets WHERE kind = $1 LIMIT 1),
		   (SELECT COALESCE(SUM(e.amount), 0) FROM ledger_entries e
		      JOIN ledger_transactions t ON t.id = e.txn_id
		      WHERE t.kind = 'topup' AND e.amount > 0)`,
		ledger.SysPlatformRevenue,
	).Scan(&ov.Users, &ov.Agents, &ov.ActiveMatches, &ov.FinishedMatches,
		&ov.OpenDisputes, &ov.PendingWithdrawals, &ov.PlatformRevenue, &ov.TopupVolume)
	if err != nil {
		return adminapi.Overview{}, err
	}
	ov.GeneratedAt = time.Now().UTC()
	return ov, nil
}
