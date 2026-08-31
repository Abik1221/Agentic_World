-- LOCK SAFETY: adding a nullable-with-default boolean is metadata-only on PostgreSQL 11+
-- (no table rewrite, no full-table lock held while rows are touched). lock_timeout is set
-- anyway so this can never be the statement that wedges the database behind a long read.
SET lock_timeout = '5s';

-- matches.private: a waiting match reachable only by somebody holding its id.
--
-- WHY THIS EXISTS. `CreateOpen` already produces exactly the shape a "room" needs — a
-- waiting match, staked, seat 0 taken, joinable by public id, with ErrSameOwner refusing
-- the creator's own account. What it does not have is INVISIBILITY.
--
-- ListWaiting returns every waiting match for a game, so an open lobby entry is a table
-- anyone browsing can take. That is correct for the open lobby and wrong for a room: if
-- two developers agree to play and one shares a code, a stranger refreshing the lobby can
-- claim the seat first. The room would work exactly once and then look broken, and the
-- person who lost the seat would have no way to tell what happened.
--
-- So the room is the same match with one bit set, and the lobby query skips it. Joining is
-- unchanged: Join() fetches by public id and never consults the listing, which is why a
-- private match is reachable by anyone holding the code and by nobody else.
--
-- DEFAULT false, so every existing row and every existing caller keeps today's behaviour.
-- The open lobby is not a room, and nothing about it changes.
ALTER TABLE matches ADD COLUMN IF NOT EXISTS private BOOLEAN NOT NULL DEFAULT false;

-- Partial index over the rows the lobby query actually scans.
--
-- ListWaiting filters `status = 'waiting'` and now `NOT private`. Indexing only that slice
-- keeps it small: waiting matches are a tiny and short-lived fraction of the table, while
-- finished ones accumulate forever.
CREATE INDEX IF NOT EXISTS matches_open_lobby_idx
    ON matches (game, bid, created_at DESC)
    WHERE status = 'waiting' AND NOT private;
