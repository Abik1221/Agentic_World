DROP INDEX IF EXISTS idx_bound_decisions_move;

ALTER TABLE agent_match_bound_decisions
    DROP COLUMN IF EXISTS extracted_move,
    DROP COLUMN IF EXISTS completion_hash,
    DROP COLUMN IF EXISTS bind_receipt;
