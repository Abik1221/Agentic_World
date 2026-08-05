-- 0075_decision_started_at — the timestamp a trace WATERFALL needs.
--
-- 0073/0074 gave each decision its inputs, outputs and latency. That supports a latency
-- bar (how long each turn took) but NOT a timeline (when each turn happened), and the
-- two answer different questions.
--
-- The existing created_at is the row's WRITE time. Every decision in a match is written
-- together when the match-benchmark event is handled, so those values are effectively
-- identical — a waterfall drawn from created_at is a flat vertical line, and one drawn
-- from cumulative latency invents the gaps between turns.
--
-- The GAPS are the point. An agent that answers in 200ms inside an 8-second phase window
-- looks fine on a latency bar and is obviously not the bottleneck on a timeline. Without
-- a real per-decision anchor a developer cannot tell "my agent is slow" from "my agent
-- waits a long time to be asked".
--
-- Nullable: matches recorded before this column existed have no honest value for it, and
-- the read path renders the timeline only when it has real offsets rather than
-- back-filling a guess.
--
-- Derivation: the producer sets it to (record time − latency), i.e. when the engine ASKED
-- for the move. See benchmark.DecisionDetail.At.

BEGIN;

ALTER TABLE agent_match_decisions
    ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ;

COMMIT;
