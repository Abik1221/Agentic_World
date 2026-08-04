-- Reverse 0071. Dropping the column loses the device names but no credential: the
-- keys themselves (prefix, hash, revoked_at) are untouched, so every logged-in
-- machine stays logged in. Note that rolling back does NOT restore the
-- one-key-per-agent invariant — any extra live keys issued while 0071 was applied
-- remain live and valid; they simply become unnamed again.

BEGIN;

DROP INDEX IF EXISTS idx_agent_keys_agent_label_live;

ALTER TABLE agent_keys
    DROP COLUMN IF EXISTS label;

COMMIT;
