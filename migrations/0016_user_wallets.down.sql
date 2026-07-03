ALTER TABLE wallets DROP CONSTRAINT IF EXISTS wallets_protected_balance_nonneg;
ALTER TABLE wallets ADD CONSTRAINT wallets_protected_balance_nonneg
    CHECK (kind NOT IN ('agent', 'escrow') OR balance >= 0);

DROP INDEX IF EXISTS idx_wallets_user;
ALTER TABLE wallets DROP COLUMN IF EXISTS user_id;
