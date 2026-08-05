BEGIN;
DROP INDEX IF EXISTS idx_agent_match_decisions_agent_recent;
DROP TABLE IF EXISTS agent_match_decisions;
COMMIT;
