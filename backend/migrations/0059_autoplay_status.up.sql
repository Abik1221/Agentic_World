-- 0059_autoplay_status — surface WHY a deployed agent's auto-play is (or isn't)
-- playing, so a developer isn't left guessing when it goes quiet. The reconciler
-- records its last decision per agent; the API returns it and `pyyol autoplay status`
-- shows it. Written only on a change, so a happily-playing agent incurs no churn.
--   last_status        : playing | searching | paused | blocked (machine code)
--   last_status_reason : human-readable detail (e.g. "agent offline", "daily loss-stop")
--   last_status_at     : when that status was last recorded
ALTER TABLE agent_autoplay ADD COLUMN IF NOT EXISTS last_status        TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_autoplay ADD COLUMN IF NOT EXISTS last_status_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_autoplay ADD COLUMN IF NOT EXISTS last_status_at     TIMESTAMPTZ;
