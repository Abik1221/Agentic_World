-- Retire Monopoly.
--
-- The arena is being withdrawn before beta. Its own tables go, its configuration rows
-- come out of the shared ones, and — the part that has to happen first — the abandoned
-- tables still marked active are closed.
--
-- # Why the stuck matches come first
--
-- max_concurrent_matches counts `matches.status = 'active'` with NO game filter
-- (store/wallet_repo.go ActiveMatchCount). A Monopoly table that nobody finished stays
-- active forever, so it permanently occupies its agent's only slot — and blocks that
-- agent from playing Goofspiel or Mafia. Observed exactly that: `pyyol play goofspiel`
-- refused six times with
--
--     limit_max_concurrent_matches: Already in 2 active matches (limit 1)
--
-- while the two matches holding the slot were abandoned Monopoly tables, one of them
-- 2034 events and six hours old. Removing the sweeper stopped them being advanced; it
-- did not close them, so without this they would block their agents indefinitely.
--
-- 'aborted' rather than 'finished': these games did not reach a conclusion, and the
-- ledger and wallet queries already treat aborted as terminal
-- (`status NOT IN ('finished','aborted')`), so nothing needs teaching about it.
--
-- # What is deliberately NOT deleted
--
-- Historical Monopoly rows in `matches`, `match_players`, the ledger and the benchmark
-- tables stay. They are financial and audit history: deleting them would orphan ledger
-- entries and rating rows that reference them, and this repo's rule is that a ledger is
-- never repaired automatically. Withdrawing the arena means it cannot be PLAYED, not
-- that it was never played.
--
-- Only UNSTAKED tables are aborted here for the same reason. A staked table in flight
-- has coins in escrow, and releasing escrow is the wallet service's job, not a
-- migration's. If any exist they are left alone and will show up in the escrow
-- reconciliation worker, which is where a human should see them.

-- 1. Close abandoned Monopoly tables so they stop holding their agents' match slots.
UPDATE matches
   SET status = 'aborted',
       finished_at = COALESCE(finished_at, now())
 WHERE game = 'monopoly'
   AND status = 'active'
   AND COALESCE(bid, 0) = 0;

-- 2. Remove Monopoly from the shared configuration tables. The tables themselves are
--    shared with Goofspiel and Mafia and must survive untouched — only the rows go.
DELETE FROM game_stakes WHERE game = 'monopoly';

-- Queue entries for an arena that can no longer be joined would otherwise sit forever.
DELETE FROM group_queue WHERE game = 'monopoly';

-- 3. Drop the Monopoly-only tables. Dependants first: monopoly_events and the team
--    join table reference the others, so the order matters even with CASCADE absent.
DROP TABLE IF EXISTS monopoly_events;
DROP TABLE IF EXISTS monopoly_properties;
DROP TABLE IF EXISTS monopoly_team_agents;
DROP TABLE IF EXISTS monopoly_teams;
DROP TABLE IF EXISTS monopoly_matches;
