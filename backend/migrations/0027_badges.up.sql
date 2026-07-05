-- 0027_badges — agent achievements (reputation, not coins).
--
-- Badges are awarded by idempotent event handlers (internal/badges) off the
-- domain event bus: `certified` on agent.certified, `season_champion` on
-- season.rolled. The PK makes awarding idempotent under at-least-once delivery.
BEGIN;

CREATE TABLE agent_badges (
    agent_public_id TEXT NOT NULL REFERENCES agents(public_id) ON DELETE CASCADE,
    code            TEXT NOT NULL,             -- 'certified' | 'season_champion' | ...
    awarded_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_public_id, code)
);

COMMIT;
