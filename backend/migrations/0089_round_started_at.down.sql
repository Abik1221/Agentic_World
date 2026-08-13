-- Dropping the column returns every reader to the reconstruction it already falls back to
-- when the value is NULL, so a rollback degrades rather than breaks.
SET lock_timeout = '5s';

ALTER TABLE matches DROP COLUMN IF EXISTS round_started_at;
