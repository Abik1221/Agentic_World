-- Purge synthetic bot traffic from the lab database.
--
-- KEEP RULE: a match is kept if it has at least one row in agent_model_calls — that is, a
-- model call was actually recorded against it through the gateway. Everything else is
-- deterministic bot traffic generated to exercise the engines. 83 matches are kept out of
-- 31,869.
--
-- WHY TRUNCATE AND RESTORE RATHER THAN DELETE. The keep set is 5,129 rows out of 50,556,862
-- in match_events and 5,695 out of 10,572,042 in agent_match_decisions. A DELETE would write
-- ~60M row versions to the WAL, hold locks for minutes, leave the space needing a VACUUM
-- FULL, and cook a laptop that is already thermally limited. Staging 11k rows, truncating,
-- and putting them back is seconds and reclaims the space immediately.
--
-- The cascade set was established by rehearsal, not by reading: TRUNCATE matches CASCADE was
-- run inside a rolled-back transaction and reported exactly 11 child tables, all staged below.
--
-- WHAT THIS DELIBERATELY DOES NOT PRESERVE:
--   * ratings / rating_updates / match_rating_changes / rank snapshots / developer_pindex —
--     every one of these was FITTED OVER THE MATCHES BEING DELETED. Keeping them would leave
--     a leaderboard whose numbers no surviving match can explain, which is worse than an
--     empty one. They are cleared so the workers recompute from what remains.
--   * group_queue / matchmaking_queue — transient state pointing at matches that will not
--     exist. Cleared entirely.

\set ON_ERROR_STOP on

BEGIN;
SET lock_timeout = '10s';

-- The keep set, by both keys: public_id for the text-keyed tables, id for the FK children.
CREATE TEMP TABLE kp(pid text PRIMARY KEY) ON COMMIT DROP;
INSERT INTO kp SELECT DISTINCT match_id FROM agent_model_calls
  WHERE match_id IS NOT NULL AND match_id <> '';
CREATE TEMP TABLE ki(id bigint PRIMARY KEY) ON COMMIT DROP;
INSERT INTO ki SELECT id FROM matches WHERE public_id IN (SELECT pid FROM kp);

-- ── stage ────────────────────────────────────────────────────────────────────
CREATE TEMP TABLE s_matches              ON COMMIT DROP AS SELECT * FROM matches              WHERE id       IN (SELECT id FROM ki);
CREATE TEMP TABLE s_match_events         ON COMMIT DROP AS SELECT * FROM match_events         WHERE match_id IN (SELECT id FROM ki);
CREATE TEMP TABLE s_match_players        ON COMMIT DROP AS SELECT * FROM match_players        WHERE match_id IN (SELECT id FROM ki);
CREATE TEMP TABLE s_mafia_seats          ON COMMIT DROP AS SELECT * FROM mafia_seats          WHERE match_id IN (SELECT id FROM ki);
CREATE TEMP TABLE s_agent_timing_samples ON COMMIT DROP AS SELECT * FROM agent_timing_samples WHERE match_id IN (SELECT id FROM ki);
CREATE TEMP TABLE s_clips                ON COMMIT DROP AS SELECT * FROM clips                WHERE match_id IN (SELECT id FROM ki);
CREATE TEMP TABLE s_disputes             ON COMMIT DROP AS SELECT * FROM disputes             WHERE match_id IN (SELECT id FROM ki);
CREATE TEMP TABLE s_fraud_flags          ON COMMIT DROP AS SELECT * FROM fraud_flags          WHERE match_id IN (SELECT id FROM ki);
CREATE TEMP TABLE s_move_signatures      ON COMMIT DROP AS SELECT * FROM move_signatures      WHERE match_id IN (SELECT id FROM ki);
CREATE TEMP TABLE s_payout_holds         ON COMMIT DROP AS SELECT * FROM payout_holds         WHERE match_id IN (SELECT id FROM ki);

CREATE TEMP TABLE s_amd  ON COMMIT DROP AS SELECT * FROM agent_match_decisions       WHERE match_id IN (SELECT pid FROM kp);
CREATE TEMP TABLE s_amb  ON COMMIT DROP AS SELECT * FROM agent_match_benchmark       WHERE match_id IN (SELECT pid FROM kp);
CREATE TEMP TABLE s_ambd ON COMMIT DROP AS SELECT * FROM agent_match_bound_decisions WHERE match_id IN (SELECT pid FROM kp);
CREATE TEMP TABLE s_amvc ON COMMIT DROP AS SELECT * FROM agent_match_verified_cost   WHERE match_id IN (SELECT pid FROM kp);
CREATE TEMP TABLE s_amr  ON COMMIT DROP AS SELECT * FROM agent_move_rejections       WHERE match_id IN (SELECT pid FROM kp);
CREATE TEMP TABLE s_awd  ON COMMIT DROP AS SELECT * FROM agent_webhook_deliveries    WHERE match_public_id IN (SELECT pid FROM kp);
CREATE TEMP TABLE s_hs   ON COMMIT DROP AS SELECT * FROM held_settlements            WHERE match_public_id IN (SELECT pid FROM kp);
CREATE TEMP TABLE s_qe   ON COMMIT DROP AS SELECT * FROM queue_events                WHERE match_public_id IN (SELECT pid FROM kp);
-- agent_model_calls IS the keep rule, so every row in it is by definition kept — staged
-- anyway because truncating it and restoring from itself is simpler than special-casing it.
CREATE TEMP TABLE s_amc  ON COMMIT DROP AS SELECT * FROM agent_model_calls;

-- ── truncate ─────────────────────────────────────────────────────────────────
-- matches CASCADE takes the 11 FK children with it (rehearsed, listed above).
TRUNCATE matches CASCADE;
TRUNCATE agent_match_decisions, agent_match_benchmark, agent_match_bound_decisions,
         agent_match_verified_cost, agent_move_rejections, agent_webhook_deliveries,
         held_settlements, queue_events, agent_model_calls;
-- Derived-from-deleted-matches. Cleared, not preserved — see the header.
TRUNCATE ratings, rating_rank_snapshots, developer_pindex, developer_pindex_history, pindex_dirty;
-- Transient queue state pointing at matches that no longer exist.
TRUNCATE group_queue, matchmaking_queue;

-- ── restore ──────────────────────────────────────────────────────────────────
-- OVERRIDING SYSTEM VALUE on every table whose id is GENERATED ALWAYS: matches,
-- match_events, agent_timing_samples, clips, disputes, fraud_flags,
-- agent_webhook_deliveries, queue_events. Without it the insert is rejected outright.
INSERT INTO matches              OVERRIDING SYSTEM VALUE SELECT * FROM s_matches;
INSERT INTO match_events         OVERRIDING SYSTEM VALUE SELECT * FROM s_match_events;
INSERT INTO agent_timing_samples OVERRIDING SYSTEM VALUE SELECT * FROM s_agent_timing_samples;
INSERT INTO clips                OVERRIDING SYSTEM VALUE SELECT * FROM s_clips;
INSERT INTO disputes             OVERRIDING SYSTEM VALUE SELECT * FROM s_disputes;
INSERT INTO fraud_flags          OVERRIDING SYSTEM VALUE SELECT * FROM s_fraud_flags;
INSERT INTO agent_webhook_deliveries OVERRIDING SYSTEM VALUE SELECT * FROM s_awd;
INSERT INTO queue_events         OVERRIDING SYSTEM VALUE SELECT * FROM s_qe;

INSERT INTO match_players     SELECT * FROM s_match_players;
INSERT INTO mafia_seats       SELECT * FROM s_mafia_seats;
INSERT INTO move_signatures   SELECT * FROM s_move_signatures;
INSERT INTO payout_holds      SELECT * FROM s_payout_holds;

INSERT INTO agent_model_calls           SELECT * FROM s_amc;
INSERT INTO agent_match_decisions       SELECT * FROM s_amd;
INSERT INTO agent_match_benchmark       SELECT * FROM s_amb;
INSERT INTO agent_match_bound_decisions SELECT * FROM s_ambd;
INSERT INTO agent_match_verified_cost   SELECT * FROM s_amvc;
INSERT INTO agent_move_rejections       SELECT * FROM s_amr;
INSERT INTO held_settlements            SELECT * FROM s_hs;

-- ── assert before commit ─────────────────────────────────────────────────────
-- The purge is only correct if EVERY surviving match still has its model calls. A restore
-- that silently dropped rows would look identical to a successful purge in the row counts.
DO $$
DECLARE m int; c int; orphan int; want int;
BEGIN
  -- The expected count is DERIVED from the staged keep set, not hardcoded. A literal
  -- would have to be re-edited every time the lab plays another match, and the failure
  -- mode of a stale literal is aborting a correct purge — or worse, passing one that
  -- kept the wrong rows because the number happened to match.
  SELECT count(*) INTO want FROM s_matches;
  SELECT count(*) INTO m FROM matches;
  SELECT count(DISTINCT match_id) INTO c FROM agent_model_calls WHERE match_id <> '';
  SELECT count(*) INTO orphan FROM agent_model_calls a
    WHERE a.match_id <> '' AND NOT EXISTS (SELECT 1 FROM matches m2 WHERE m2.public_id = a.match_id);
  IF m <> want THEN RAISE EXCEPTION 'expected % matches after purge, found %', want, m; END IF;
  IF c <> want THEN RAISE EXCEPTION 'expected % matches with model calls, found %', want, c; END IF;
  IF orphan <> 0 THEN RAISE EXCEPTION '% model calls reference a match that did not survive', orphan; END IF;
  RAISE NOTICE 'purge OK: % matches kept, all with model calls, no orphans', m;
END $$;

COMMIT;
