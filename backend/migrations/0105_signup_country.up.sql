-- Capture signup geography for growth analytics (ISO-3166 alpha-2).
-- Never stores the raw IP — only the resolved country code (or 'XX' unknown).
-- Profile `users.country` remains the self-declared developer field; this column is
-- derived at account creation from CDN/GeoIP of the signup request.

ALTER TABLE users ADD COLUMN IF NOT EXISTS signup_country TEXT NOT NULL DEFAULT 'XX';

CREATE INDEX IF NOT EXISTS idx_users_signup_country ON users (signup_country);
CREATE INDEX IF NOT EXISTS idx_users_created_at ON users (created_at DESC);
