-- 0070_agent_limit_defaults_allow_ranked — a new agent must be able to enter the
-- cheapest ranked table.
--
-- THE BUG. Every agent was created with coin_limit_per_match = 100 and max_bid = 100
-- (migration 0002). The cheapest ranked stake is 500 coins: migration 0038 seeds
-- goofspiel/monopoly Low at 100, but the platform's $5 minimum-stake floor
-- (gamestakes.DefaultMinStakeUSDCents = 500 cents, at a 1¢ peg) lifts every paid tier to
-- at least 500 coins. So the affordability preflight refused every newly-registered
-- developer's first ranked join with
--
--     409 limit_coin_limit_per_match — "Bid 500 exceeds the per-match limit of 100."
--
-- Nobody could compete for real out of the box. The two numbers were set years apart and
-- nothing tied them together, so neither side looked wrong on its own.
--
-- WHY THE DEFAULTS MOVE AND NOT THE FLOOR. The floor is a deliberate economic decision —
-- below it the rake rounds to nothing while the match still costs real inference spend,
-- which also makes a cheap table the cheapest way to farm ranked activity. The agent
-- defaults are simply stale: they predate the floor and were never revisited.
--
-- The new values are derived from the floor rather than picked:
--   coin_limit_per_match 500 — exactly one minimum-stake table
--   max_bid              500 — the same, so the two cannot disagree
--   daily_loss_limit    2000 — four losses at the minimum stake before the day stops
--   session_loss_limit  1000 — two, so a bad session halts sooner than a bad day
-- min_wallet_balance stays at 50: a reserve is meant to be small, and it is the one
-- guardrail that was never in conflict with anything.
--
-- EXISTING ROWS ARE NOT TOUCHED, deliberately. A stored 100 cannot be told apart from a
-- deliberate 100 — an owner who chose a tight per-match cap must not have it widened by a
-- deploy, because that is their money at stake and widening a risk limit without being
-- asked is the one direction that is never safe. Agents created before this migration
-- raise their own limit from Strategy (the figure is typed there now), and the operator
-- can see exactly which guardrail is blocking an agent via
-- GET /v1/admin/users/{id}/agents.

BEGIN;

ALTER TABLE agents
    ALTER COLUMN coin_limit_per_match SET DEFAULT 500,
    ALTER COLUMN max_bid             SET DEFAULT 500,
    ALTER COLUMN daily_loss_limit    SET DEFAULT 2000,
    ALTER COLUMN session_loss_limit  SET DEFAULT 1000;

COMMIT;
