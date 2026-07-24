-- 0059_autoplay_status (down)
ALTER TABLE agent_autoplay DROP COLUMN IF EXISTS last_status;
ALTER TABLE agent_autoplay DROP COLUMN IF EXISTS last_status_reason;
ALTER TABLE agent_autoplay DROP COLUMN IF EXISTS last_status_at;
