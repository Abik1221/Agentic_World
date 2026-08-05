BEGIN;
ALTER TABLE agent_match_decisions
    DROP COLUMN IF EXISTS input_json,
    DROP COLUMN IF EXISTS input_truncated;
COMMIT;
