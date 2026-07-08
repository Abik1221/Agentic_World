BEGIN;

DROP INDEX IF EXISTS idx_withdrawals_broadcasted;

ALTER TABLE withdrawals
    DROP COLUMN IF EXISTS chain,
    DROP COLUMN IF EXISTS dest_wallet_address;

COMMIT;
