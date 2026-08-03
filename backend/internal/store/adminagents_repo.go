package store

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/adminapi"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminAgentsRepo serves the operator's per-user AGENT view: each agent, the guardrails it
// runs under, and how close it currently is to hitting them.
//
// See internal/adminapi/useragents.go for why this exists — in short, "why did my agent stop
// playing?" is almost always a guardrail doing its job, and no operator surface could see a
// single one of those limits.
type AdminAgentsRepo struct{ db *pgxpool.Pool }

func NewAdminAgentsRepo(db *pgxpool.Pool) *AdminAgentsRepo { return &AdminAgentsRepo{db: db} }

var _ adminapi.AgentsRepo = (*AdminAgentsRepo)(nil)

// userAgentsQuery reads the agents plus their live usage in ONE round trip.
//
// The usage figures are lateral sub-selects rather than a second pass per agent: an operator
// opening a user with six agents should not cost thirteen queries, and — more importantly —
// the limit and the usage it is compared against must come from the same snapshot. Read
// separately, an agent could settle a match between the two reads and be reported as "500
// coins down against a 500 limit, not blocked".
//
// Loss windows match the guardrails they are checked against exactly (see
// wallet.Service.LossToday and CheckJoin):
//   - daily: matches finished since local midnight, which is the boundary the daily stop uses
//   - session: the same figure over the last 12 hours, matching session_loss_limit's window
//
// House agents are excluded: they are platform-run opponents, never a developer's work.
const userAgentsQuery = `
WITH u AS (SELECT id, public_id FROM users WHERE public_id = $1)
SELECT
  a.public_id, a.name, a.slug, a.status,
  COALESCE(a.verification_level, ''), COALESCE(a.framework, ''), a.created_at,
  a.coin_limit_per_match, a.max_bid, a.min_wallet_balance,
  a.daily_loss_limit, a.session_loss_limit,
  a.max_concurrent_matches, a.cooldown_losses, a.cooldown_seconds, a.auto_join,
  -- Coins lost today (non-negative), the figure the daily stop reads.
  COALESCE((SELECT -SUM(mp.coins_delta) FROM match_players mp
              JOIN matches m ON m.id = mp.match_id
             WHERE mp.agent_id = a.id AND m.status = 'finished'
               AND m.finished_at >= date_trunc('day', now())
               AND mp.coins_delta < 0), 0)::bigint AS loss_today,
  COALESCE((SELECT -SUM(mp.coins_delta) FROM match_players mp
              JOIN matches m ON m.id = mp.match_id
             WHERE mp.agent_id = a.id AND m.status = 'finished'
               AND m.finished_at >= now() - interval '12 hours'
               AND mp.coins_delta < 0), 0)::bigint AS loss_session,
  COALESCE((SELECT COUNT(*) FROM match_players mp
              JOIN matches m ON m.id = mp.match_id
             WHERE mp.agent_id = a.id AND m.status IN ('waiting','active')), 0)::int AS active_matches,
  COALESCE((SELECT wl.balance FROM wallets wl WHERE wl.agent_id = a.id), 0)::bigint AS balance
FROM agents a
JOIN u ON u.id = a.owner_user_id
WHERE a.kind <> 'house'
ORDER BY a.created_at, a.id`

func (r *AdminAgentsRepo) UserAgents(
	ctx context.Context, userPublicID string,
) ([]adminapi.AgentGuardrails, bool, error) {
	// The user must be confirmed to EXIST separately from having agents.
	//
	// Without this, an unknown id and a real developer who has not created an agent both
	// return an empty list — so an operator typing a wrong id is shown a plausible-looking
	// empty profile instead of "no such user".
	var exists bool
	if err := r.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE public_id = $1)`, userPublicID).Scan(&exists); err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, nil
	}

	rows, err := r.db.Query(ctx, userAgentsQuery, userPublicID)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()

	out := make([]adminapi.AgentGuardrails, 0, 4)
	for rows.Next() {
		var a adminapi.AgentGuardrails
		var createdAt time.Time
		if err := rows.Scan(
			&a.Agent, &a.Name, &a.Slug, &a.Status,
			&a.VerificationLevel, &a.Framework, &createdAt,
			&a.CoinLimitPerMatch, &a.MaxBid, &a.MinWalletBalance,
			&a.DailyLossLimit, &a.SessionLossLimit,
			&a.MaxConcurrentMatches, &a.CooldownLosses, &a.CooldownSeconds, &a.AutoJoin,
			&a.LossToday, &a.LossSession, &a.ActiveMatches, &a.Balance,
		); err != nil {
			return nil, true, err
		}
		a.CreatedAt = createdAt
		out = append(out, a)
	}
	return out, true, rows.Err()
}
