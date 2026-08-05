-- 0074_decision_input — the INPUT half of a per-decision trace.
--
-- 0073 stored what the agent DID (action, outcome, latency, its own rationale, tokens).
-- This stores what it was GIVEN: the turn view the engine handed it for that decision.
--
-- WHY IT MATTERS. "Reasoned X, played 9, illegal" is not actionable. The same line
-- beside the state the agent was looking at is a bug report — the developer can see
-- whether the model misread the board, whether the board was missing something it
-- needed, or whether the reasoning simply did not follow. Every serious LLM
-- observability tool is built on the input↔output pair; this platform had only outputs.
--
-- SIZE. This is by far the largest column in the table — a mid-game Monopoly view
-- carries the whole board, and a long match has hundreds of decisions. The producer
-- (internal/benchmark) caps a single view at 16 KiB and a seat's total at 512 KiB, and
-- sets input_truncated when it drops one. JSONB rather than TEXT so the shape is
-- queryable later without a migration, and TOAST keeps the row narrow for the common
-- reads that never select it.
--
-- PRIVACY. A turn view contains the seat's HIDDEN information: its own hand in
-- Goofspiel, its role in Mafia. It is strictly more sensitive than the rationale beside
-- it. Every read path is scoped to the caller's own agents at the query — see
-- DevTraceRepo.MatchDecisions — and this column must never be joined into a spectator,
-- replay or leaderboard surface.

BEGIN;

ALTER TABLE agent_match_decisions
    ADD COLUMN IF NOT EXISTS input_json      JSONB,
    -- Distinguishes "the agent was handed nothing" from "we did not keep what it was
    -- handed". Rendering both as an empty pane would make the first look like an engine
    -- bug and send a developer chasing a problem that does not exist.
    ADD COLUMN IF NOT EXISTS input_truncated BOOLEAN NOT NULL DEFAULT false;

COMMIT;
