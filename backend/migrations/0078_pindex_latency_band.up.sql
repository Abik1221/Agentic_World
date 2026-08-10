-- 0078_pindex_latency_band — recalibrate the P-Index speed curve to latencies that
-- actually occur, and ship it INACTIVE so an operator decides when it takes effect.
--
-- THE PROBLEM, measured rather than assumed.
--
-- The active config scores speed across 500ms → 8,000ms: an agent at or under half a
-- second gets full credit, one at or over eight seconds gets none. Against the platform's
-- real distribution that band is calibrated for a world which does not exist here:
--
--     p50   6,988 ms      <- more than HALF of all honest decisions sit at the cutoff
--     p90  23,161 ms
--     p99  55,011 ms
--     max  55,013 ms
--
-- So the speed component is saturated for most of the platform. Worse, it cannot
-- discriminate where discrimination matters: a 23-second agent and a 55-second agent both
-- score exactly zero, as does one that is simply broken. Latency is 20% of the Intelligence
-- dimension and it was carrying almost no information.
--
-- That matters more now than it did. Adaptive decision windows (internal/deadline) let a
-- slow local model or a reasoning model take the time it genuinely needs, because the
-- platform can tell "thinking" from "dead" by probing. The deliberate trade is that
-- slowness costs REPUTATION rather than the match — an agent gets to make its own move,
-- and pays for the wait in its index. That trade only works if the index can actually see
-- the difference between 10 seconds and 50.
--
-- THE NEW BAND: 2,000 ms → 60,000 ms.
--
--   2s   is a genuinely quick agent — a small fast model with a tight prompt — rather than
--        a number that only a cache hit could reach.
--   60s  is the top of the observed range and lines up with the shot clock, so the curve
--        spends its resolution on the interval agents actually occupy.
--
-- Under it: ~7s scores about 0.91, ~23s about 0.64, ~55s about 0.09. A thoughtful agent is
-- no longer indistinguishable from a broken one, which is the entire point.
--
-- INACTIVE ON PURPOSE. The P-Index is public and drives reputation; flipping a weight
-- underneath every developer without a deliberate decision is not something a migration
-- should do. Activate with:
--
--     UPDATE pindex_config SET active = (version = 3);
--
-- Derived from the newest config that actually HAS an intelligence block, rather than
-- transcribed, so every other parameter carries over exactly and cannot drift from a
-- hand-copied literal.
--
-- Deliberately not "the active row": on a freshly migrated database the active config is
-- v1, which predates the intelligence dimension entirely. Deriving from it produced a v3
-- with no intelligence block at all — jsonb_set cannot create a path whose parent is
-- missing — so the new band silently vanished. Caught by running the migration against a
-- fresh database rather than only against the lab, where v2 happened to be active.

BEGIN;

INSERT INTO pindex_config (version, params, active)
SELECT 3,
       jsonb_set(
         jsonb_set(params, '{intelligence,latency_fast_ms}', '2000'::jsonb, true),
         '{intelligence,latency_slow_ms}', '60000'::jsonb, true),
       false
  FROM pindex_config
 WHERE params ? 'intelligence'
 ORDER BY version DESC
 LIMIT 1
ON CONFLICT (version) DO NOTHING;

COMMIT;
