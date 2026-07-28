BEGIN;

-- Restore 0037's seeded Mafia ladder ($1 / $5 / $20 at the 1¢ peg).
--
-- Only the exact values this migration wrote are reverted, so an operator who
-- re-priced their tiers afterwards does not have their work silently undone by a
-- rollback. Tiers for other games are left alone: the up-migration lifted them to a
-- floor without recording what they were before, so there is nothing honest to
-- restore them to.
UPDATE game_stakes SET coins = 100,  updated_at = now()
 WHERE game = 'mafia' AND tier_key = 'low'  AND coins = 500;
UPDATE game_stakes SET coins = 500,  updated_at = now()
 WHERE game = 'mafia' AND tier_key = 'mid'  AND coins = 2000;
UPDATE game_stakes SET coins = 2000, updated_at = now()
 WHERE game = 'mafia' AND tier_key = 'high' AND coins = 5000;

COMMIT;
