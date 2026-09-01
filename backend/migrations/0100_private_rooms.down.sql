-- Dropping the column makes every room visible in the open lobby rather than losing it:
-- rooms are ordinary waiting matches, so a rollback degrades them to open tables instead of
-- stranding them. Any room waiting at that moment becomes joinable by a stranger, which is
-- worth knowing before rolling back with rooms in flight.
SET lock_timeout = '5s';

DROP INDEX IF EXISTS matches_open_lobby_idx;
ALTER TABLE matches DROP COLUMN IF EXISTS private;
