BEGIN;
DROP INDEX IF EXISTS idx_agent_match_decisions_unscored;
ALTER TABLE agent_match_decisions
    DROP COLUMN IF EXISTS skill_regret,
    DROP COLUMN IF EXISTS skill_best,
    DROP COLUMN IF EXISTS skill_scorer_version;
COMMIT;
