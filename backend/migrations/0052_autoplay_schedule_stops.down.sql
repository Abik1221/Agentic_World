-- 0052_autoplay_schedule_stops (down)
ALTER TABLE agent_autoplay DROP COLUMN IF EXISTS active_from_utc;
ALTER TABLE agent_autoplay DROP COLUMN IF EXISTS active_until_utc;
ALTER TABLE agent_autoplay DROP COLUMN IF EXISTS daily_match_cap;
ALTER TABLE agent_autoplay DROP COLUMN IF EXISTS daily_token_budget;
ALTER TABLE agent_autoplay DROP COLUMN IF EXISTS take_profit_coins;
ALTER TABLE agent_autoplay DROP COLUMN IF EXISTS daily_loss_stop;
