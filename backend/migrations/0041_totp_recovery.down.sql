BEGIN;

ALTER TABLE users DROP COLUMN IF EXISTS totp_recovery_hashes;

COMMIT;
