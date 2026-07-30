BEGIN;

-- Repair the tier ladders 0065 knowingly left collided.
--
-- 0065 gave Mafia a considered ladder ($5/$20/$50) but sent every other game through
-- a catch-all: `UPDATE game_stakes SET coins = 500 WHERE coins < 500`. Goofspiel and
-- Monopoly carry the SAME seed as Mafia (100 / 500 / 2000), so that lifted `low` to
-- 500 and left it sitting exactly on `mid`.
--
-- Its own comment predicted this and deferred it — "the admin UI will surface it on
-- the next save". It does not, in the way that matters: nothing rejects EXISTING
-- rows, only new writes. So the ladder stayed live and a developer reading
-- /v1/games/goofspiel/stakes saw two different tiers priced identically, with no way
-- to tell what `mid` was for. An outside tester hit exactly that and reported it.
--
-- Same fix Mafia got, for the games that share its seed: shift the whole ladder up a
-- notch so the original spacing survives, rather than inventing a new one.
--
-- Guarded all-or-nothing on the post-0065 state (500 / 500 / 2000), so a deployment
-- that has since priced its own tiers is untouched. A partially re-priced ladder
-- keeps what the operator chose.
UPDATE game_stakes AS gs
   SET coins = v.new_coins, updated_at = now()
  FROM (VALUES
          ('low',   500::BIGINT,  500::BIGINT),
          ('mid',   500::BIGINT,  2000::BIGINT),
          ('high',  2000::BIGINT, 5000::BIGINT)
       ) AS v(tier_key, old_coins, new_coins)
 WHERE gs.game IN ('goofspiel', 'monopoly')
   AND gs.tier_key = v.tier_key
   AND gs.coins = v.old_coins
   -- Only when this game still holds the exact collided ladder 0065 produced.
   AND (SELECT count(*) FROM game_stakes s
         WHERE s.game = gs.game
           AND (s.tier_key, s.coins) IN (('low', 500), ('mid', 500), ('high', 2000))) = 3;

COMMIT;
