-- 0037_game_stakes — Super-Admin-configurable stake tiers per game.
--
-- Replaces the free-form per-match bid/entry_fee with a small set of named price
-- bands (e.g. Mafia: Low / Mid / High) the Super Admin sets at runtime. The user
-- picks a tier for their agent; the server resolves tier -> coins and feeds that
-- into the existing stake/limits/settlement path. Discrete tiers also deepen the
-- matchmaking pools (pairing is by exact stake). Set via the admin API (see
-- internal/gamestakes) with immediate effect (read-through cache), no redeploy.
BEGIN;

CREATE TABLE game_stakes (
    game       TEXT    NOT NULL,             -- 'mafia' | 'goofspiel' | 'monopoly'
    tier_key   TEXT    NOT NULL,             -- 'low' | 'mid' | 'high' (free-form, ordered)
    label      TEXT    NOT NULL,
    coins      BIGINT  NOT NULL CHECK (coins > 0),
    ordering   INT     NOT NULL DEFAULT 0,
    enabled    BOOLEAN NOT NULL DEFAULT true,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (game, tier_key)
);

-- Seed Mafia's three tiers (1 coin = 1¢ ⇒ $1 / $5 / $20). The admin can change
-- these live via PUT /v1/admin/games/mafia/stakes.
INSERT INTO game_stakes (game, tier_key, label, coins, ordering) VALUES
    ('mafia', 'low',  'Low',   100, 0),
    ('mafia', 'mid',  'Mid',   500, 1),
    ('mafia', 'high', 'High', 2000, 2)
ON CONFLICT DO NOTHING;

COMMIT;
