-- 0063_mafia_house_bots — dedicated house bots for Mafia sandbox push-play.
--
-- Mafia sandbox needs a full 12-seat table: the developer's agent + 11 fillers.
-- Previously those fillers were the DEMO agents, so mafia push-play only worked when
-- DEMO_BOTS was on — which is off in prod (to keep the live arena clean), leaving mafia
-- sandbox unavailable (501). These are permanent kind='house' agents (like the Goofspiel
-- house opponents in 0017_sandbox): kind='house' keeps them out of the leaderboard,
-- matchmaking queue, dev profiles, and open lobby, and they only ever play in a
-- user-initiated sandbox match — never autonomously. Idempotent on re-run.

-- 11 house fillers (RosterSize-1 for the 12-player mafia table). status=active +
-- verified_bot so they render naturally in match views/replays; kind=house hides them
-- everywhere competitive.
INSERT INTO agents (public_id, owner_user_id, name, slug, description, framework, status, verification_level, kind)
SELECT v.public_id, u.id, v.name, v.slug,
       'Engine-driven Mafia house bot — fills a sandbox seat, never staked or rated.',
       'arena-house', 'active', 'verified_bot', 'house'
FROM users u,
    (VALUES
        ('ag_house_mafia_01', 'House Townsfolk 1',  'house-mafia-01'),
        ('ag_house_mafia_02', 'House Townsfolk 2',  'house-mafia-02'),
        ('ag_house_mafia_03', 'House Townsfolk 3',  'house-mafia-03'),
        ('ag_house_mafia_04', 'House Townsfolk 4',  'house-mafia-04'),
        ('ag_house_mafia_05', 'House Townsfolk 5',  'house-mafia-05'),
        ('ag_house_mafia_06', 'House Townsfolk 6',  'house-mafia-06'),
        ('ag_house_mafia_07', 'House Townsfolk 7',  'house-mafia-07'),
        ('ag_house_mafia_08', 'House Townsfolk 8',  'house-mafia-08'),
        ('ag_house_mafia_09', 'House Townsfolk 9',  'house-mafia-09'),
        ('ag_house_mafia_10', 'House Townsfolk 10', 'house-mafia-10'),
        ('ag_house_mafia_11', 'House Townsfolk 11', 'house-mafia-11')
    ) AS v(public_id, name, slug)
WHERE u.public_id = 'usr_system'
ON CONFLICT (public_id) DO NOTHING;

-- Zero-balance wallets (house bots never stake or earn).
INSERT INTO wallets (agent_id, kind, balance)
SELECT a.id, 'agent', 0
FROM agents a
WHERE a.public_id LIKE 'ag_house_mafia_%'
  AND NOT EXISTS (SELECT 1 FROM wallets w WHERE w.agent_id = a.id);
