-- 0024_fix_system_wallet_uniqueness — allow more than one developer to sign up.
--
-- Bug: idx_wallets_system_kind (from 0001) is UNIQUE(kind) WHERE agent_id IS NULL.
-- When 0016_user_wallets introduced USER wallets (agent_id NULL, user_id set,
-- kind='user'), they were swept into that same partial index — so only ONE user
-- wallet could exist platform-wide, and the *second* signup failed with a 23505.
--
-- A "system" wallet is one that belongs to neither an agent nor a user
-- (treasury/clearing/escrow/bad_debt): agent_id IS NULL AND user_id IS NULL.
-- User wallets are already uniquely constrained by idx_wallets_user
-- (UNIQUE(user_id) WHERE user_id IS NOT NULL). Tighten the system index so it
-- only governs true system wallets.

BEGIN;

DROP INDEX IF EXISTS idx_wallets_system_kind;

CREATE UNIQUE INDEX IF NOT EXISTS idx_wallets_system_kind
    ON wallets (kind)
    WHERE agent_id IS NULL AND user_id IS NULL;

COMMIT;
