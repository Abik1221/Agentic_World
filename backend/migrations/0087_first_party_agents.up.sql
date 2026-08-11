-- first_party marks an agent the PLATFORM operates, as distinct from one a
-- developer brought.
--
-- It exists because there is a third category the schema could not express. `kind`
-- already separates 'house' — the platform's own bots, which never stake and are
-- deliberately absent from every ranked surface — from everything else. But the
-- pre-launch arena also holds agents the platform ran itself through the real
-- gateway, with real provider keys, staking and competing normally. Their play is
-- genuine and gateway-verified, so hiding it as 'house' would be false; showing it
-- unmarked beside developers' agents would be false in the other direction.
--
-- So this is a LABEL, not a filter. A first-party agent ranks on the same terms as
-- any other — same publishedAgent gate, same ratings, same P-Index — and the
-- surfaces that display it say whose it is. The alternative, an unmarked board of
-- platform agents, is the thing the exclusion census already exists to prevent: a
-- number that reads as something it is not.
--
-- Defaults false, so every existing agent keeps its current meaning and no board
-- changes until an import sets it deliberately.
ALTER TABLE agents
  ADD COLUMN IF NOT EXISTS first_party boolean NOT NULL DEFAULT false;

-- Partial: the flag is true for a small minority, and every query that cares asks
-- for exactly that minority.
CREATE INDEX IF NOT EXISTS idx_agents_first_party
  ON agents (id) WHERE first_party;
