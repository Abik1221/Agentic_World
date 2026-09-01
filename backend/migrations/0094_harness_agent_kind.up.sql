-- 0094_harness_agent_kind — a third agent kind for the platform's own benchmark.
--
-- `house` already exists and is not the right home for this. A house bot fills a table so a
-- real developer has an opponent; it is deterministic, it never stakes, and it is exempt
-- from certification precisely because it is not claiming to be LLM-backed.
--
-- A harness agent is the opposite on the one axis that matters: it IS LLM-backed, that is
-- the entire point, and its decisions are the measurement. It must certify like any real
-- agent, its model calls must be bound like any real agent's, and the only things that make
-- it different are that the platform runs it and its results never touch a user-facing
-- board.
--
-- Reusing `house` would have bought the exemptions we do NOT want (certification) and none
-- of the separation we do. So: a third kind, and the public sinks were switched to an
-- allowlist in 0093's sibling change so this one is invisible to them by default rather
-- than by remembering to exclude it.
ALTER TABLE agents DROP CONSTRAINT IF EXISTS agents_kind_check;
ALTER TABLE agents ADD CONSTRAINT agents_kind_check
    CHECK (kind = ANY (ARRAY['external'::text, 'house'::text, 'harness'::text]));

COMMENT ON COLUMN agents.kind IS
    'external = a developer''s agent, the only kind on public boards. '
    'house = a platform bot that fills tables; deterministic, never stakes, exempt from '
    'certification. '
    'harness = a platform benchmark agent; LLM-backed and certified like a real agent, but '
    'its matches are unrated, zero-stake, and excluded from every user-facing surface.';

-- Deliberately NO stake trigger here.
--
-- The first draft of this migration added one, against a table that turned out to be the
-- tier CONFIG rather than any per-agent stake — it would have been dead code that read like
-- a guarantee, which is worse than no guarantee at all.
--
-- The platform already has a working precedent for exactly this rule: houseStake() returns
-- 0 in Go and is pinned by TestHouseStakeIsZero and TestNoHardcodedStakeReachesAHouseTable.
-- The harness rule belongs beside those, enforced where the stake is actually decided,
-- rather than in a schema object that cannot see the decision.
