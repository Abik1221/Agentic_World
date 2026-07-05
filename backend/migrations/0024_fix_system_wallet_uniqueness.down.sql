-- Restore the original (buggy) predicate.
BEGIN;

DROP INDEX IF EXISTS idx_wallets_system_kind;

CREATE UNIQUE INDEX IF NOT EXISTS idx_wallets_system_kind
    ON wallets (kind)
    WHERE agent_id IS NULL;

COMMIT;
