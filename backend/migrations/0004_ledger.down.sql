DROP TABLE IF EXISTS ledger_entries;
DROP TABLE IF EXISTS ledger_transactions;

-- Restore the original blanket non-negative invariant and drop the agent FK +
-- backfilled agent wallets, returning wallets to its Stage 0 shape.
ALTER TABLE wallets DROP CONSTRAINT IF EXISTS wallets_protected_balance_nonneg;
DELETE FROM wallets WHERE kind = 'agent';
ALTER TABLE wallets DROP CONSTRAINT IF EXISTS fk_wallets_agent;
ALTER TABLE wallets ADD CONSTRAINT wallets_balance_nonneg CHECK (balance >= 0);
