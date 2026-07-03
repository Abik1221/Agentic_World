-- 0018_password_auth — email + password sign-in.
--
-- Adds a bcrypt password hash to users so an owner can sign up and log in with a
-- normal email + password, alongside the existing X-claim onboarding and
-- passwordless magic-link recovery. The column is NULLABLE on purpose: users
-- created via the X-claim flow (0002) have no password, and that stays valid.
--
-- The `email` column already exists (CITEXT UNIQUE, migration 0002), so no new
-- uniqueness constraint is needed here — one email maps to at most one owner.

BEGIN;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS password_hash TEXT;

-- Fast lookup of password-enabled accounts during login. Partial: only rows that
-- actually carry a password are indexed (X-only owners are skipped).
CREATE INDEX IF NOT EXISTS idx_users_email_pw
    ON users (email)
    WHERE password_hash IS NOT NULL;

COMMIT;
