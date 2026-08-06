DROP INDEX IF EXISTS idx_decisions_agent_scaffold;
ALTER TABLE agent_match_decisions
    DROP COLUMN IF EXISTS scaffold,
    DROP COLUMN IF EXISTS scaffold_unstable;
