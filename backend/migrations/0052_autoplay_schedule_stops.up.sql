-- 0052_autoplay_schedule_stops — owner-configured "when to play / when to stop"
-- for a deployed agent's auto-play, on top of the hard wallet guardrails.
--   active_from_utc / active_until_utc : hours 0–23 window (equal ⇒ always;
--                                        from>until ⇒ wraps midnight)
--   daily_match_cap                    : stop after N matches today (0 = off)
--   daily_token_budget                 : stop when today's LLM token spend hits this
--   take_profit_coins                  : stop for the day at net ≥ +this coins
--   daily_loss_stop                    : stop for the day at net ≤ −this coins
ALTER TABLE agent_autoplay ADD COLUMN IF NOT EXISTS active_from_utc    INT    NOT NULL DEFAULT 0;
ALTER TABLE agent_autoplay ADD COLUMN IF NOT EXISTS active_until_utc   INT    NOT NULL DEFAULT 0;
ALTER TABLE agent_autoplay ADD COLUMN IF NOT EXISTS daily_match_cap    INT    NOT NULL DEFAULT 0;
ALTER TABLE agent_autoplay ADD COLUMN IF NOT EXISTS daily_token_budget BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_autoplay ADD COLUMN IF NOT EXISTS take_profit_coins  BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_autoplay ADD COLUMN IF NOT EXISTS daily_loss_stop    BIGINT NOT NULL DEFAULT 0;
