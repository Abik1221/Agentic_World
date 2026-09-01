-- Removes the platform lab benchmark: its seeded matches, its agents, and its agent kind.
--
-- LOCK SAFETY: the deletes are bounded to harness-owned rows, which are a few thousand at
-- most. lock_timeout so this can never be the statement that wedges the database behind a
-- long read.
SET lock_timeout = '5s';

-- # Why rows and not tables
--
-- There were never any lab tables. `harnessseed` wrote its published results into the SAME
-- tables real play uses — matches, match_players, match_events, agents and the per-decision
-- benchmark tables — so the lab benchmark exists as platform-run matches sitting alongside
-- developer ones, distinguished only by agents.kind = 'harness'.
--
-- That is what makes this a delete rather than a drop, and it is also why the order below
-- matters: the child rows have to go before the matches and agents they reference.
--
-- # Why deleting is safe for everything that stays
--
-- Harness matches were unrated by design and the public sinks already excluded them by
-- allowlist (see 0093/0094), so nothing user-facing counted them:
--
--   * The developer model board requires m.rated. Harness matches never were.
--   * Ratings and ELO read rated matches only.
--   * P-Index reads per-decision benchmark rows for EXTERNAL agents.
--
-- So this removes the lab's data and leaves every developer-facing number exactly where it
-- was. The developer model board will look emptier only in the sense that it never contained
-- these rows in the first place.

-- Child rows first, scoped through the agents that own them.
DELETE FROM agent_model_calls
 WHERE agent_id IN (SELECT id FROM agents WHERE kind = 'harness');

DELETE FROM agent_match_bound_decisions
 WHERE agent_id IN (SELECT id FROM agents WHERE kind = 'harness');

DELETE FROM agent_match_verified_cost
 WHERE agent_id IN (SELECT id FROM agents WHERE kind = 'harness');

DELETE FROM agent_match_decisions
 WHERE agent_id IN (SELECT id FROM agents WHERE kind = 'harness');

DELETE FROM agent_match_benchmark
 WHERE agent_id IN (SELECT id FROM agents WHERE kind = 'harness');

-- The matches themselves. Identified as matches EVERY seat of which was a harness agent,
-- which is the same structural rule the harness board used for eligibility. A match with a
-- mixed table is not a lab match and must not be touched — there should be none, and phrasing
-- it this way means a stray one survives rather than being silently deleted.
CREATE TEMP TABLE lab_matches ON COMMIT DROP AS
SELECT m.id, m.public_id
  FROM matches m
 WHERE EXISTS (
         SELECT 1 FROM match_players mp
           JOIN agents a ON a.id = mp.agent_id
          WHERE mp.match_id = m.id AND a.kind = 'harness')
   AND NOT EXISTS (
         SELECT 1 FROM match_players mp
           JOIN agents a ON a.id = mp.agent_id
          WHERE mp.match_id = m.id AND a.kind <> 'harness');

DELETE FROM match_events  WHERE match_id  IN (SELECT id FROM lab_matches);
DELETE FROM match_players WHERE match_id  IN (SELECT id FROM lab_matches);
DELETE FROM matches       WHERE id        IN (SELECT id FROM lab_matches);

-- The board history series the harness board wrote. Keyed by board name since 0093, which is
-- what lets this remove one series without touching the developer one.
DELETE FROM model_board_history WHERE board = 'harness';

-- The agents last, now that nothing references them.
DELETE FROM agents WHERE kind = 'harness';

-- Retire the kind itself, so nothing can create one again.
--
-- Done as a constraint change rather than left permissive: a kind that no code produces but
-- the schema still accepts is a door somebody re-opens by accident later, and the whole point
-- of this change is that there is no lab benchmark any more.
ALTER TABLE agents DROP CONSTRAINT IF EXISTS agents_kind_check;
ALTER TABLE agents ADD CONSTRAINT agents_kind_check
    CHECK (kind = ANY (ARRAY['external'::text, 'house'::text]));
