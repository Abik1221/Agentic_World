BEGIN;

ALTER TABLE users
    DROP COLUMN IF EXISTS totp_secret_enc,
    DROP COLUMN IF EXISTS totp_enabled,
    DROP COLUMN IF EXISTS totp_confirmed_at;

COMMIT;
