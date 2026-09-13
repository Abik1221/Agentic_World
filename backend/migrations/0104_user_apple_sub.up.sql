-- Sign in with Apple: stable subject (`sub` from Apple's id_token) as the link key.
-- Partial unique index matches google_sub / github_id — many users will never link Apple.
ALTER TABLE users ADD COLUMN IF NOT EXISTS apple_sub TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS users_apple_sub_key ON users (apple_sub) WHERE apple_sub IS NOT NULL;
