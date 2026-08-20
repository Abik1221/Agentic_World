-- Two indexes for two of the heaviest read paths in the arena. Both were found by measuring
-- pg_stat_statements against a populated database, and one of them corrected a diagnosis
-- that three plausible theories had got wrong.

-- 1. THE SKILL SCORER'S RESCORE PHASE.
--
-- NextUnscored asked for "no score at the current scorer version" as one condition,
--
--     skill_scorer_version IS NULL OR skill_scorer_version < $1
--
-- and it cost 93 seconds and 4.7 GB per batch: 188 GB over 40 batches. What it was NOT:
-- not a sequential scan (an index was used throughout), not TOAST (input_json averages
-- 2 KB, so a 500-row batch is about a megabyte), and not bloat (vacuuming from 594,089
-- dead tuples to 570 changed nothing measurable).
--
-- It was the ORDER BY. The planner served "ORDER BY match_id, agent_id, seq LIMIT 500" from
-- the primary key — precisely those columns — and applied the predicate afterwards on the
-- heap. match_id order is approximately age order and the oldest decisions are the ones
-- already scored, so every batch walked hundreds of thousands of scored rows before finding
-- 500 unscored ones, and the walk gets longer as more work is completed. A cost that grows
-- with progress.
--
-- The partial index for the never-scored rows already existed and could not be used, because
-- the OR did not match its predicate. Splitting the query in two lets each phase match an
-- index: the never-scored phase uses idx_agent_match_decisions_unscored (the same scan drops
-- from 560,000 buffers to 6,372, because a partial index does not contain the scored prefix
-- at all), and the rescore phase uses the index below.
--
-- Restricted to rows that HAVE a score, so it stays the mirror image of the never-scored
-- index rather than overlapping it — together they cover the eligible set exactly once, and
-- neither carries rows the other already indexes.
CREATE INDEX IF NOT EXISTS idx_decisions_stale_score
    ON agent_match_decisions (skill_scorer_version, match_id, agent_id, seq)
 WHERE input_json IS NOT NULL AND skill_scorer_version IS NOT NULL;

-- idx_agent_match_decisions_unscored is deliberately KEPT and is now actually reachable:
-- it is what makes the never-scored phase cheap. It had 108 lifetime scans not because it
-- was the wrong index but because no query could use it.

-- 2. THE ROUND-DEADLINE SWEEPER.
--
--     WHERE status='active' AND game=$1 AND round_deadline <= $2 ORDER BY round_deadline
--
-- idx_matches_sweep is (round_deadline) WHERE status='active', so it served the range and the
-- ordering but had nothing to say about `game`. Every tick walked EVERY active match in
-- deadline order and threw away the other games: 10,140 calls for 15 GB of reads. There is a
-- sweeper per game and they all run on a timer, which is how a small per-call cost became a
-- large total.
--
-- Leading with game makes the equality a seek and leaves round_deadline ordered within it, so
-- a sweep for one game touches only that game's rows and the ORDER BY still needs no sort.
--
-- idx_matches_sweep is KEPT: it still serves the sweep variants that do not filter by game.
CREATE INDEX IF NOT EXISTS idx_matches_sweep_game
    ON matches (game, round_deadline)
 WHERE status = 'active';
