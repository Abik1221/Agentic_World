-- Restore the collided ladder 0065 left behind (low and mid both at 500). Guarded so
-- a re-priced deployment is untouched.
BEGIN;
UPDATE game_stakes AS gs
   SET coins = v.old_coins, updated_at = now()
  FROM (VALUES
          ('mid',   2000::BIGINT, 500::BIGINT),
          ('high',  5000::BIGINT, 2000::BIGINT)
       ) AS v(tier_key, new_coins, old_coins)
 WHERE gs.game IN ('goofspiel', 'monopoly')
   AND gs.tier_key = v.tier_key
   AND gs.coins = v.new_coins;
COMMIT;
