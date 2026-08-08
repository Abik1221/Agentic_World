package store

import (
	"context"
	"errors"
	"fmt"
	mafia "github.com/agent-arena/arena/internal/engine/mafia"
	"log/slog"

	"github.com/agent-arena/arena/internal/demo"
	"github.com/agent-arena/arena/internal/identity"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/wallet"
	"github.com/jackc/pgx/v5"
)

// EnsureDevAgents creates or loads rule-based demo agents (no LLM). Idempotent on slug.
// houseStakeCap is what a house bot may stake per match. Tracks the cheapest ranked tier so
// these agents can sit at the tables they create.
const houseStakeCap = 500

func (r *IdentityRepo) EnsureDevAgents(ctx context.Context, n int, mint *wallet.Service, log *slog.Logger) ([]demo.Agent, error) {
	out := make([]demo.Agent, 0, n)
	// House-bot limits, not user limits.
	//
	// The per-match and per-bid caps track the cheapest ranked tier so these agents can actually
	// sit at the tables they create; they were seeded at 100 before the floor moved to 500, which
	// blocks every join outright.
	//
	// MaxConcurrentMatches is the one that mattered most. DefaultLimits caps it at 1, which is a
	// USER protection — it stops a developer's agent staking several pots at once. Applied to a
	// house bot it is nonsense: Mafia needs 12 distinct seats, so a pool where each member may
	// hold one match cannot seat a single table while any Goofspiel game is running. The result
	// was 190 aborted Mafia tables against 1 finished, every one holding exactly one seat.
	//
	// RosterSize+2 mirrors the pool sizing (12 Mafia seats + 2 Goofspiel) so one bot can hold a
	// Mafia seat and still take a Goofspiel table without the roster fill failing.
	// EVERY limit here is a user protection that becomes nonsense on a house bot, and each one
	// blocked the Mafia roster in turn — concurrency first, then balance, then session loss.
	// Fixing them one at a time just moved the error message, so they are set together as a
	// coherent house profile:
	//
	//   per-match / max-bid  track the cheapest ranked tier, or the bot cannot sit at the table
	//                        it just created (seeded at 100 against a 500 floor)
	//   concurrency          RosterSize+2: Mafia needs 12 DISTINCT seats, and a pool capped at
	//                        one match each cannot seat one table while Goofspiel runs
	//   session/daily loss   a house bot is SUPPOSED to lose — it plays both sides of every
	//                        table. DefaultLimits allows two losses a session at this stake,
	//                        which stops the bot within minutes of starting
	//   cooldown             a losing streak is the normal state for a population playing
	//                        itself; pausing on it stalls the only thing generating matches
	//
	// A developer's agent keeps every one of these. They exist to protect someone's money, and a
	// house bot has none of its own — it is topped up when it runs dry (see demo.TopUp).
	limits := identity.DefaultLimits()
	limits.CoinLimitPerMatch = houseStakeCap
	limits.MaxBid = houseStakeCap
	limits.MaxConcurrentMatches = mafia.RosterSize + 2
	// Loss and cooldown caps are left at the DEFAULTS on purpose. They were raised when house
	// bots staked; a house that stakes nothing cannot lose a coin, so a raised limit protects
	// against nothing and reads as a safeguard that is doing work it is not. Concurrency stays
	// raised because it is not about money — Mafia needs 12 DISTINCT seats and the pool must be
	// able to hold them.
	limits.CooldownLosses = 0
	limits.CooldownSeconds = 0
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
		if err == nil {
			// HEAL an existing row. EnsureDevAgents is idempotent on slug, so agents seeded
			// before the floor moved kept coin_limit_per_match=100 and max_concurrent_matches=1
			// forever — the exact rows that made Mafia unplayable.
			//
			// Migration 0070 deliberately does NOT backfill limits like this, and it is right:
			// for a DEVELOPER's agent a stored 100 cannot be told apart from a deliberate 100,
			// and widening someone's risk cap by deploy is never safe. These are house bots.
			// Nobody chose these values, they are infrastructure, and leaving them stale breaks
			// a game rather than protecting anyone.
			if _, uerr := r.db.Exec(ctx,
				`UPDATE agents SET coin_limit_per_match=$2, max_bid=$3, max_concurrent_matches=$4,
				        session_loss_limit=$5, daily_loss_limit=$6, cooldown_losses=$7, cooldown_seconds=$8
				  WHERE slug=$1 AND (coin_limit_per_match<>$2 OR max_bid<>$3 OR max_concurrent_matches<>$4
				     OR session_loss_limit<>$5 OR daily_loss_limit<>$6)`,
				slug, limits.CoinLimitPerMatch, limits.MaxBid, limits.MaxConcurrentMatches,
				limits.SessionLossLimit, limits.DailyLossLimit,
				limits.CooldownLosses, limits.CooldownSeconds); uerr != nil {
				return nil, uerr
			}
		}
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

	// Labelled 'house-bot' so these show up as what they are in any key audit, and so
	// they can never collide with a developer's device label.
	if _, err = tx.Exec(ctx,
		`INSERT INTO agent_keys (agent_id, key_prefix, key_hash, scope, label)
		 VALUES ($1,$2,$3,'agent','house-bot')`,
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

// HouseBotBalances returns every house bot's agent-wallet balance, keyed by agent public id.
//
// Reads the wallet rather than any cached figure: the top-up decision is about what the bot can
// actually stake right now, and a stale number would either starve a broke bot or mint for one
// that is fine.
func (r *IdentityRepo) HouseBotBalances(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id, coalesce(w.balance, 0)
		   FROM agents a
		   LEFT JOIN wallets w ON w.agent_id = a.id AND w.kind = 'agent'
		  WHERE a.slug LIKE 'demo%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var bal int64
		if err := rows.Scan(&id, &bal); err != nil {
			return nil, err
		}
		out[id] = bal
	}
	return out, rows.Err()
}
