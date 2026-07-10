-- 0038_game_stakes_seed_all — seed the remaining games' stake tiers so every game
-- ships with Low/Mid/High out of the box (0037 seeded Mafia). The Super Admin can
-- change any of these live via PUT /v1/admin/games/{game}/stakes.
BEGIN;

INSERT INTO game_stakes (game, tier_key, label, coins, ordering) VALUES
    ('goofspiel', 'low',  'Low',   100, 0),
    ('goofspiel', 'mid',  'Mid',   500, 1),
    ('goofspiel', 'high', 'High', 2000, 2),
    ('monopoly',  'low',  'Low',   100, 0),
    ('monopoly',  'mid',  'Mid',   500, 1),
    ('monopoly',  'high', 'High', 2000, 2)
ON CONFLICT DO NOTHING;

COMMIT;
