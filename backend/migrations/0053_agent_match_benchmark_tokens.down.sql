-- 0053 (down)
DROP INDEX IF EXISTS idx_agent_match_benchmark_agent_day;
ALTER TABLE agent_match_benchmark DROP COLUMN IF EXISTS tokens;
