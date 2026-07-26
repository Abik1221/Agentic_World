-- Google Identity Services (ID-token) login: link an account to its stable Google
-- subject id. Nullable + unique — existing email/password accounts are unaffected.
ALTER TABLE users ADD COLUMN IF NOT EXISTS google_sub TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS users_google_sub_key ON users (google_sub) WHERE google_sub IS NOT NULL;
