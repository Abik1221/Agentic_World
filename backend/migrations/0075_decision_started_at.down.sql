BEGIN;
ALTER TABLE agent_match_decisions DROP COLUMN IF EXISTS started_at;
COMMIT;
