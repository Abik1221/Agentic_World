-- 0053 — token spend per (match, agent) so the auto-play daily token budget can
-- sum "tokens spent today". Additive to the 0051 table.
ALTER TABLE agent_match_benchmark ADD COLUMN IF NOT EXISTS tokens BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_agent_match_benchmark_agent_day ON agent_match_benchmark (agent_id, updated_at);
