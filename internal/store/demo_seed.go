package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/agent-arena/arena/internal/demo"
	"github.com/agent-arena/arena/internal/identity"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/wallet"
	"github.com/jackc/pgx/v5"
)

// EnsureDevAgents creates or loads rule-based demo agents (no LLM). Idempotent on slug.
func (r *IdentityRepo) EnsureDevAgents(ctx context.Context, n int, mint *wallet.Service, log *slog.Logger) ([]demo.Agent, error) {
	out := make([]demo.Agent, 0, n)
	limits := identity.DefaultLimits()
	limits.CoinLimitPerMatch = 500
	limits.MaxBid = 500
	limits.AutoJoin = true

	for i := 0; i < n; i++ {
		slug := demoDevSlug(i)
		name := demo.DevAgentName(i)
		var agentPublicID, ownerPublicID string
		err := r.db.QueryRow(ctx,
			`SELECT a.public_id, u.public_id FROM agents a
			 JOIN users u ON u.id = a.owner_user_id
			 WHERE a.slug = $1`, slug).
			Scan(&agentPublicID, &ownerPublicID)
		if errors.Is(err, pgx.ErrNoRows) {
			agentPublicID, ownerPublicID, err = r.insertDevAgent(ctx, slug, name, limits, i)
			if err != nil {
				return nil, err
			}
			if log != nil {
				log.Info("demo agent seeded", "agent", agentPublicID, "name", name)
			}
		} else if err != nil {
			return nil, err
		}
		if mint != nil {
			_ = demo.MintCoins(ctx, mint, agentPublicID, 10_000)
		}
		out = append(out, demo.Agent{PublicID: agentPublicID, OwnerPublicID: ownerPublicID, Name: name})
	}
	return out, nil
}

func demoDevSlug(i int) string { return fmt.Sprintf("demo-bot-%02d", i+1) }

func (r *IdentityRepo) insertDevAgent(ctx context.Context, slug, name string, limits identity.Limits, i int) (agentPublicID, ownerPublicID string, err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ownerPublicID = platform.NewID(platform.PrefixUser)
	xUser := fmt.Sprintf("dev:demo:%02d", i+1)
	xHandle := fmt.Sprintf("@demo_%02d", i+1)
	var userID int64
	if err = tx.QueryRow(ctx,
		`INSERT INTO users (public_id, x_user_id, x_handle) VALUES ($1,$2,$3) RETURNING id`,
		ownerPublicID, xUser, xHandle).Scan(&userID); err != nil {
		return "", "", err
	}

	agentPublicID = platform.NewID(platform.PrefixAgent)
	var agentID int64
	if err = tx.QueryRow(ctx,
		`INSERT INTO agents (public_id, owner_user_id, name, slug, description, framework,
		     status, verification_level, coin_limit_per_match, daily_loss_limit, session_loss_limit,
		     min_wallet_balance, max_concurrent_matches, cooldown_losses, cooldown_seconds, max_bid, auto_join)
		 VALUES ($1,$2,$3,$4,$5,$6,'active','trusted',$7,$8,$9,$10,$11,$12,$13,$14,$15)
		 RETURNING id`,
		agentPublicID, userID, name, slug, "Rule-based demo agent (no LLM)", demo.FrameworkLabel,
		limits.CoinLimitPerMatch, limits.DailyLossLimit, limits.SessionLossLimit, limits.MinWalletBalance,
		limits.MaxConcurrentMatches, limits.CooldownLosses, limits.CooldownSeconds, limits.MaxBid, limits.AutoJoin).
		Scan(&agentID); err != nil {
		return "", "", err
	}

	if _, err = tx.Exec(ctx,
		`INSERT INTO agent_keys (agent_id, key_prefix, key_hash, scope) VALUES ($1,$2,$3,'agent')`,
		agentID, "demo_"+slug, "dev-no-key"); err != nil {
		return "", "", err
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO wallets (agent_id, kind, balance) VALUES ($1,'agent',0)`, agentID); err != nil {
		return "", "", err
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO wallets (user_id, kind, balance) VALUES ($1,'user',0)`, userID); err != nil {
		return "", "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return agentPublicID, ownerPublicID, nil
}
