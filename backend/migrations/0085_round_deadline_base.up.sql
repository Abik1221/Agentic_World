-- round_deadline_base: the deadline as FIRST set for the current round, which an extension
-- never moves.
--
-- WHY THIS IS NEEDED. tryExtend bounds how long a round may be held open. It derived
-- "extensions granted so far" from elapsed time:
--
--     elapsed := now - (round_deadline - window)
--     granted := (elapsed - window) / Extension
--
-- and the comment above it claimed the policy ceiling therefore bounded everything, with no
-- counter needed. That reasoning is circular: the origin it measures from is round_deadline,
-- which the extension ITSELF pushes forward. So after each extension `elapsed` snaps back to
-- roughly one window, `granted` never climbs, and the ceiling is never reached.
--
-- Measured on a live staked table: Goofspiel allows MaxExtensions=3 and the match was granted
-- 17, sitting on one round for twelve minutes where the honest equivalent played all thirteen
-- rounds in three and a half.
--
-- This was reachable before completion binding only in theory, because a healthy agent's move
-- was always accepted and no round stayed unsealed while its endpoint answered /health.
-- Completion binding creates the first ordinary trigger: an agent whose every move is refused
-- looks, to the deadline logic, exactly like one that is still thinking. That turns a rejected
-- cheat into a way to stall a staked table other people have money on.
--
-- WHY A BASE TIMESTAMP AND NOT AN EXTENSION COUNTER. A counter answers only this one question.
-- A fixed origin makes `elapsed` TRUTHFUL, so the ceiling, the extension count and anything
-- later added all read the same real time-on-round — one fix rather than a constant per
-- consumer. It is also self-checking: base > round_deadline can never be legitimate, so a
-- missed reset is detectable rather than silent.
--
-- NULLABLE, and every reader falls back to round_deadline when it is absent. Rows already
-- mid-flight when this ships have no base, and inventing one would either grant them a fresh
-- extension budget or expire them instantly; falling back preserves exactly today's behaviour
-- for them and correct behaviour for every round that starts afterwards.
ALTER TABLE matches
    ADD COLUMN IF NOT EXISTS round_deadline_base timestamptz;

-- Backfill from the current deadline. For a round already extended this understates the time
-- already spent, so an in-flight round gets at most one more full budget rather than an
-- unbounded one — the conservative direction, and it converges the moment the round advances.
UPDATE matches
   SET round_deadline_base = round_deadline
 WHERE status = 'active'
   AND round_deadline IS NOT NULL
   AND round_deadline_base IS NULL;
