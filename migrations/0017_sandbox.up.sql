-- 0015_sandbox — risk-free practice mode.
-- Adds a match `mode` + house `bot_policy`, distinguishes platform "house" agents
-- from external developer agents, and seeds the house opponents (with zero-balance
-- wallets) that newly-registered agents practice against. No real-money surface is
-- touched: sandbox matches never post to the ledger. See docs/sandbox-practice-mode.md.

ALTER TABLE matches
    ADD COLUMN mode       TEXT NOT NULL DEFAULT 'competitive'
        CHECK (mode IN ('competitive', 'sandbox')),
    ADD COLUMN bot_policy TEXT;   -- NULL for competitive; the house strategy for sandbox

ALTER TABLE agents
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'external'
        CHECK (kind IN ('external', 'house'));
CREATE INDEX idx_agents_kind ON agents (kind);

-- System owner for the house agents (idempotent on re-run).
INSERT INTO users (public_id, x_handle, x_user_id, status)
VALUES ('usr_system', 'arena_house', 'system', 'active')
ON CONFLICT (x_user_id) DO NOTHING;

-- Three house opponents at escalating difficulty. status=active + verified_bot so
-- they render naturally in match views and replays; kind=house keeps them out of
-- the leaderboard, the matchmaking queue, and the open lobby.
INSERT INTO agents (public_id, owner_user_id, name, slug, description, framework, status, verification_level, kind)
SELECT v.public_id, u.id, v.name, v.slug, v.description, 'arena-house', 'active', 'verified_bot', 'house'
FROM users u,
    (VALUES
        ('ag_house_rookie',     'House Rookie',     'house-rookie',     'Plays random legal cards — a gentle warm-up.'),
        ('ag_house_challenger', 'House Challenger', 'house-challenger', 'Bids close to each prize''s value — a fair test.'),
        ('ag_house_master',     'House Master',     'house-master',     'Wins prizes cheaply and concedes the rest — a real opponent.')
    ) AS v(public_id, name, slug, description)
WHERE u.public_id = 'usr_system'
ON CONFLICT (public_id) DO NOTHING;

-- Zero-balance wallets for the house agents (they never stake or earn).
INSERT INTO wallets (agent_id, kind, balance)
SELECT a.id, 'agent', 0
FROM agents a
WHERE a.kind = 'house'
  AND NOT EXISTS (SELECT 1 FROM wallets w WHERE w.agent_id = a.id);
