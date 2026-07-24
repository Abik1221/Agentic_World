-- 0056_agent_match_benchmark_cost — structural economics per (match, agent) so the
-- profile can compute cost-to-win, per-game cost, and lifetime cost with plain SQL.
-- Fed from the same match.benchmark fact that already carries these (SeatSummary).
ALTER TABLE agent_match_benchmark ADD COLUMN IF NOT EXISTS game           TEXT             NOT NULL DEFAULT '';
ALTER TABLE agent_match_benchmark ADD COLUMN IF NOT EXISTS estimated_cost DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE agent_match_benchmark ADD COLUMN IF NOT EXISTS result         TEXT             NOT NULL DEFAULT '';
