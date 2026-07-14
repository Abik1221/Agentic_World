-- 0040_user_totp — free, self-hosted TOTP two-factor for step-up auth on money
-- movement (withdrawals + withdrawal-wallet changes). The secret is stored
-- ENCRYPTED at rest (secretbox); totp_enabled flips true only after the user
-- confirms a first code, so a half-finished enrollment never gates anything.
BEGIN;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS totp_secret_enc   BYTEA,
    ADD COLUMN IF NOT EXISTS totp_enabled      BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS totp_confirmed_at TIMESTAMPTZ;

COMMIT;
