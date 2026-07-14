-- 0041_totp_recovery — backup/recovery codes for TOTP 2FA, so losing an
-- authenticator device is never a permanent lockout from cash-out (a required
-- best-practice UX for any 2FA that gates money). Stored as HMAC hashes of the
-- one-time codes (never plaintext); a code is consumed by removing its hash.
BEGIN;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS totp_recovery_hashes TEXT[];

COMMIT;
