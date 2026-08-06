-- 0077_decision_skill — per-decision DECISION QUALITY, stored so it can be recomputed.
--
-- 0073/0074/0075 gave each decision its inputs, its action, its cost and its timing. All
-- of that describes what HAPPENED. None of it says whether the move was any good, and
-- neither does anything else on the platform:
--
--   * rating (Glicko-2 / TrueSkill) scores OUTCOMES, so a strong agent that loses a
--     variance-heavy game is indistinguishable from a weak one;
--   * the P-Index "intelligence" dimension scores CONDUCT — legal rate, fallback rate,
--     latency — so an agent playing fast, legal, terrible moves scores full marks.
--
-- internal/skill closes that gap: it compares each decision against the best action
-- available from that exact state, computed deterministically from the engine's own
-- ground truth. These columns are where that verdict lives.
--
-- WHY STORED RATHER THAN COMPUTED ON READ
--
-- Scoring a Goofspiel bid runs regret matching over the round's payoff matrix. That is
-- far too expensive to do inside a leaderboard query, and it would be recomputed
-- identically on every page load. It is a pure function of (input_json, action), both of
-- which are already on this row — so it is computed once, offline, and cached here.
--
-- WHY A VERSION COLUMN
--
-- The scorer will improve, and when it does every stored score becomes stale. Without a
-- version there is no way to tell a score produced by today's scorer from one produced by
-- a better one six months from now, and a leaderboard silently mixing the two is
-- comparing agents against different yardsticks. scorer_version lets a worker find and
-- recompute exactly the rows that are behind, and lets a reader exclude scores it does
-- not trust. Recomputation is safe precisely because scoring is deterministic and depends
-- on nothing but this row.
--
-- NULLABLE, and that is load-bearing. NULL means "not scored", which is genuinely
-- different from "scored zero" (a decision that gave up everything). Decisions recorded
-- before input capture shipped have no view to score against and must stay NULL forever
-- rather than being back-filled with a guess — a mean that quietly includes invented
-- zeroes would rank agents on data that does not exist.

BEGIN;

ALTER TABLE agent_match_decisions
    -- Share of the achievable value this decision gave up, in [0,1]. 0 = the best action
    -- available from this state; 1 = the worst. Normalised per decision so a blunder in a
    -- 13-point round and one in a 3-point round are not counted as the same mistake.
    ADD COLUMN IF NOT EXISTS skill_regret DOUBLE PRECISION,
    -- The action that WAS best, in the game's own vocabulary. Stored for the trace UI:
    -- "you bid 6, the best bid was 12" is actionable in a way that a bare number is not.
    ADD COLUMN IF NOT EXISTS skill_best TEXT,
    -- Which scorer produced skill_regret. See above.
    ADD COLUMN IF NOT EXISTS skill_scorer_version INTEGER;

-- The worker's work queue: rows that have a view to score but no current score. Partial,
-- because the scored majority is dead weight in this index — the query only ever asks for
-- the unscored remainder, and on a table that grows with every decision on the platform
-- a full index would cost far more than it returns.
CREATE INDEX IF NOT EXISTS idx_agent_match_decisions_unscored
    ON agent_match_decisions (match_id, agent_id, seq)
    WHERE input_json IS NOT NULL AND skill_scorer_version IS NULL;

COMMIT;
