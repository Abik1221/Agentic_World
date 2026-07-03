BEGIN;

DROP INDEX IF EXISTS idx_users_email_pw;

ALTER TABLE users
    DROP COLUMN IF EXISTS password_hash;

COMMIT;
