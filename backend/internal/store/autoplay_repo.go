package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agent-arena/arena/internal/autoplay"
)

// AutoplayRepo persists per-agent auto-play availability (agent_autoplay).
// Satisfies autoplay.Repo. Agents are addressed by public id and resolved to the
// internal agents.id via subquery (mirrors the other repos).
type AutoplayRepo struct{ db *pgxpool.Pool }

func NewAutoplayRepo(db *pgxpool.Pool) *AutoplayRepo { return &AutoplayRepo{db: db} }

// Set upserts the agent's availability. A missing agent (unknown public id) makes
// the INSERT affect no rows — surfaced as an error the handler can report.
func (r *AutoplayRepo) Set(ctx context.Context, s autoplay.Setting) error {
	tag, err := r.db.Exec(ctx,
		`INSERT INTO agent_autoplay (agent_id, owner_public_id, enabled, mode, bid, games,
		   active_from_utc, active_until_utc, daily_match_cap, daily_token_budget, take_profit_coins, daily_loss_stop, updated_at)
		 VALUES ((SELECT id FROM agents WHERE public_id = $1), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
		 ON CONFLICT (agent_id) DO UPDATE SET
		   owner_public_id    = EXCLUDED.owner_public_id,
		   enabled            = EXCLUDED.enabled,
		   mode               = EXCLUDED.mode,
		   bid                = EXCLUDED.bid,
		   games              = EXCLUDED.games,
		   active_from_utc    = EXCLUDED.active_from_utc,
		   active_until_utc   = EXCLUDED.active_until_utc,
		   daily_match_cap    = EXCLUDED.daily_match_cap,
		   daily_token_budget = EXCLUDED.daily_token_budget,
		   take_profit_coins  = EXCLUDED.take_profit_coins,
		   daily_loss_stop    = EXCLUDED.daily_loss_stop,
		   updated_at         = now()`,
		s.AgentPublicID, s.OwnerPublicID, s.Enabled, string(s.Mode), s.Bid, s.Games,
		s.ActiveFromUTC, s.ActiveUntilUTC, s.DailyMatchCap, s.DailyTokenBudget, s.TakeProfitCoins, s.DailyLossStop)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("autoplay: unknown agent")
	}
	return nil
}

// Get returns the agent's setting and whether a row exists.
func (r *AutoplayRepo) Get(ctx context.Context, agentPublicID string) (autoplay.Setting, bool, error) {
	var s autoplay.Setting
	var mode string
	err := r.db.QueryRow(ctx,
		`SELECT a.public_id, ap.owner_public_id, ap.enabled, ap.mode, ap.bid, ap.games,
		        ap.active_from_utc, ap.active_until_utc, ap.daily_match_cap,
		        ap.daily_token_budget, ap.take_profit_coins, ap.daily_loss_stop
		   FROM agent_autoplay ap JOIN agents a ON a.id = ap.agent_id
		  WHERE a.public_id = $1`, agentPublicID).
		Scan(&s.AgentPublicID, &s.OwnerPublicID, &s.Enabled, &mode, &s.Bid, &s.Games,
			&s.ActiveFromUTC, &s.ActiveUntilUTC, &s.DailyMatchCap,
			&s.DailyTokenBudget, &s.TakeProfitCoins, &s.DailyLossStop)
	if errors.Is(err, pgx.ErrNoRows) {
		return autoplay.Setting{}, false, nil
	}
	if err != nil {
		return autoplay.Setting{}, false, err
	}
	s.Mode = autoplay.Mode(mode)
	return s, true, nil
}

// ListEnabled returns every agent with auto-play switched on (the reconciler's
// per-tick work list).
func (r *AutoplayRepo) ListEnabled(ctx context.Context) ([]autoplay.Setting, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id, ap.owner_public_id, ap.enabled, ap.mode, ap.bid, ap.games,
		        ap.active_from_utc, ap.active_until_utc, ap.daily_match_cap,
		        ap.daily_token_budget, ap.take_profit_coins, ap.daily_loss_stop
		   FROM agent_autoplay ap JOIN agents a ON a.id = ap.agent_id
		  WHERE ap.enabled`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []autoplay.Setting
	for rows.Next() {
		var s autoplay.Setting
		var mode string
		if err := rows.Scan(&s.AgentPublicID, &s.OwnerPublicID, &s.Enabled, &mode, &s.Bid, &s.Games,
			&s.ActiveFromUTC, &s.ActiveUntilUTC, &s.DailyMatchCap,
			&s.DailyTokenBudget, &s.TakeProfitCoins, &s.DailyLossStop); err != nil {
			return nil, err
		}
		s.Mode = autoplay.Mode(mode)
		out = append(out, s)
	}
	return out, rows.Err()
}
