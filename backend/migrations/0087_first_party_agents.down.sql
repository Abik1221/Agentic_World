DROP INDEX IF EXISTS idx_agents_first_party;
ALTER TABLE agents DROP COLUMN IF EXISTS first_party;
