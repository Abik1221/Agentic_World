-- 0047_monopoly_lobby
-- Waiting-lobby support for staked, agent-vs-agent Monopoly tables. A waiting row
-- has no engine state yet (state is NULL until it starts); target_players records
-- how many real agents must join before the table starts and stakes are escrowed.
-- Other games leave this column NULL and ignore it.
ALTER TABLE matches ADD COLUMN IF NOT EXISTS target_players INT;
