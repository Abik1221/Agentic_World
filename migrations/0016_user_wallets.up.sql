-- 0014_user_wallets — per-user treasury wallets (deposits land here; owners
-- allocate coins to agents). Aligns with the platform financial spec: users own
-- balances; agents spend allocated coins in competitions.

ALTER TABLE wallets ADD COLUMN IF NOT EXISTS user_id BIGINT REFERENCES users(id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_wallets_user ON wallets(user_id) WHERE user_id IS NOT NULL;

-- Backfill a user wallet for every existing owner.
INSERT INTO wallets (user_id, kind, balance)
SELECT u.id, 'user', 0 FROM users u
WHERE NOT EXISTS (SELECT 1 FROM wallets w WHERE w.user_id = u.id);

-- User wallets must never go negative (same as agent wallets).
ALTER TABLE wallets DROP CONSTRAINT IF EXISTS wallets_protected_balance_nonneg;
ALTER TABLE wallets ADD CONSTRAINT wallets_protected_balance_nonneg
    CHECK (kind NOT IN ('agent', 'escrow', 'user') OR balance >= 0);
