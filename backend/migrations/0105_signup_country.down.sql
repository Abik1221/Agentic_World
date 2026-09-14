DROP INDEX IF EXISTS idx_users_created_at;
DROP INDEX IF EXISTS idx_users_signup_country;
ALTER TABLE users DROP COLUMN IF EXISTS signup_country;
