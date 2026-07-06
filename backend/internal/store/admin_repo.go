package store

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/adminapi"
	"github.com/agent-arena/arena/internal/ledger"
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
		`SELECT m.public_id, m.game, m.status, m.bid, COALESCE(win.public_id, ''),
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
		if err := rows.Scan(&m.PublicID, &m.Game, &m.Status, &m.Bid, &m.WinnerAgent,
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
