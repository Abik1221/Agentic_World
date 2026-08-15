-- GitHub OAuth login: link an account to its stable GitHub numeric user id (never
-- changes, unlike the login/username). Nullable + unique — existing accounts are
-- unaffected, and a NULL means "not linked to GitHub" (Postgres allows many NULLs).
ALTER TABLE users ADD COLUMN IF NOT EXISTS github_id TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS users_github_id_key ON users (github_id) WHERE github_id IS NOT NULL;
