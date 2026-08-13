-- LOCK SAFETY. The ALTER below needs ACCESS EXCLUSIVE on matches, which queues behind any
-- in-flight read and then blocks every query that arrives after it. Migration 0088 was
-- observed waiting on exactly that lock behind the model-board fit (measured at 217s),
-- holding the whole server behind it during startup.
--
-- Adding a nullable column is cheap in Postgres 11+ (no table rewrite) — the risk is the
-- WAIT, not the work. lock_timeout makes it fail fast and loudly: a migration that errors
-- is retried in seconds, one that blocks takes the platform down with it.
SET lock_timeout = '5s';

-- round_started_at: when the current round actually began.
--
-- WHY THIS EXISTS. Think-time was RECONSTRUCTED as round_deadline minus the configured
-- move window, and both halves of that subtraction were wrong.
--
-- round_deadline MOVES. A liveness-gated extension pushes it forward (see tryExtend), so
-- after a grant the reconstructed start slid later and the measured response time came out
-- SMALLER than reality — a genuinely slow agent recorded as a fast one.
--
-- The configured window is not the window in force. Deadlines are adaptive (p95 of the
-- agent's own recent latencies times headroom, per internal/deadline), so subtracting the
-- static constant put the start too early for any agent that had earned a longer window,
-- and the measured response time came out LARGER than reality.
--
-- That measurement is not cosmetic: it feeds verification.Record, which builds the timing
-- profile used to decide whether a HUMAN is playing by hand. Inflating an honest slow
-- agent's response times pushes it toward being flagged; deflating them masks the very
-- thing the detector exists to catch. A fraud control must not be fed a derived number
-- when the real one can simply be written down.
--
-- WHY NOT DERIVE IT FROM round_deadline_base. base is the deadline as first set, so the
-- start is base minus the window used at that moment — and that window is not recoverable
-- later, because the agent's latency samples have moved on since. Recomputing it gives a
-- different number than the one actually applied.
--
-- Nullable, and every reader falls back to the old reconstruction when it is NULL: rounds
-- already in flight when this ships have no start recorded, and a missing value must
-- degrade to the previous behaviour rather than record a zero-time think of decades.
ALTER TABLE matches
    ADD COLUMN IF NOT EXISTS round_started_at TIMESTAMPTZ;
