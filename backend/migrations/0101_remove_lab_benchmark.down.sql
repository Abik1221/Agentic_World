-- Restores the 'harness' agent kind. It does NOT restore the deleted rows.
--
-- Stated plainly because a down migration that silently does half its job is worse than one
-- that says so: the lab benchmark's matches, agents and decisions are gone and this cannot
-- bring them back. They lived only in the deleted rows and in the seed dataset that was
-- removed from the repository in the same change.
--
-- Rolling back therefore gives you a schema that would accept harness agents again, and an
-- empty lab benchmark. Re-seeding would mean restoring internal/harnessseed and its dataset
-- from git history.
SET lock_timeout = '5s';

ALTER TABLE agents DROP CONSTRAINT IF EXISTS agents_kind_check;
ALTER TABLE agents ADD CONSTRAINT agents_kind_check
    CHECK (kind = ANY (ARRAY['external'::text, 'house'::text, 'harness'::text]));
