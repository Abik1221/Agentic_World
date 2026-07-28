BEGIN;

-- Bring the seeded stake tiers up to the $5 paid-table floor.
--
-- 0037 seeded Mafia at 100 / 500 / 2000 coins — $1 / $5 / $20 at the 1¢ peg. The
-- admin surface now refuses to WRITE a paid tier below $5, so those rows sat in a
-- state the API would no longer accept: an operator opening the stakes editor and
-- pressing save without changing anything would have been rejected, which is a
-- confusing way to meet a new rule.
--
-- Why a floor at all: the platform's cut is a percentage, so below some stake the
-- rake rounds toward nothing while the match still costs real inference spend. A $1
-- table is one the platform runs at a loss, and it is also the cheapest possible way
-- to farm ranked activity.
--
-- THE WHOLE LADDER MOVES, not just the offending band. Lifting only `low` to the
-- floor would collide with `mid` at 500, and tiers must strictly increase by ordering
-- (Low < Mid < High) — so a "minimal" fix would leave the table in a different
-- invalid state. Shifting all three up one notch ($5 / $20 / $50) preserves the
-- original spacing instead of inventing a new ladder.
--
-- Guarded on the exact seeded values so an operator who has already priced their own
-- tiers keeps them untouched: if ANY band differs from 0037's seed, this does nothing
-- at all and the operator's ladder stands, all-or-nothing.
--
-- The tradeoff, stated plainly: a deployment that re-priced part of the ladder and
-- left a band under $5 keeps that band, and will be told about it the next time the
-- stakes editor is saved. That is the right way round — a migration silently
-- re-pricing an operator's deliberate configuration is worse than an explicit error
-- at the moment they next touch it.
UPDATE game_stakes AS gs
   SET coins = v.new_coins, updated_at = now()
  FROM (VALUES
          ('low',   100::BIGINT,  500::BIGINT),
          ('mid',   500::BIGINT,  2000::BIGINT),
          ('high',  2000::BIGINT, 5000::BIGINT)
       ) AS v(tier_key, old_coins, new_coins)
 WHERE gs.game = 'mafia'
   AND gs.tier_key = v.tier_key
   AND gs.coins = v.old_coins
   -- All three bands must still hold their seeded values, or we touch none of them.
   AND (SELECT count(*) FROM game_stakes s
         WHERE s.game = 'mafia'
           AND (s.tier_key, s.coins) IN (('low', 100), ('mid', 500), ('high', 2000))) = 3;

-- Any OTHER game seeded below the floor is lifted to exactly the floor. Named games
-- get a considered ladder above; this is the catch-all that keeps the invariant true
-- without pretending to know what a bespoke tier was meant to cost. Where this would
-- collide with a sibling band, the admin UI will surface it on the next save — better
-- a visible conflict than a silently invented price.
UPDATE game_stakes SET coins = 500, updated_at = now()
 WHERE coins < 500 AND game <> 'mafia';

COMMIT;
