DROP INDEX IF EXISTS users_github_id_key;
ALTER TABLE users DROP COLUMN IF EXISTS github_id;
