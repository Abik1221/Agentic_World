BEGIN;

DROP TABLE IF EXISTS wallet_verify_challenges;

ALTER TABLE users
    DROP COLUMN IF EXISTS verified_wallet_address,
    DROP COLUMN IF EXISTS wallet_verified_at;

COMMIT;
