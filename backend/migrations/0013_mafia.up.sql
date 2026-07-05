-- 0013_mafia — hidden roles and Mafia-specific seat metadata.
-- Reuses matches / match_players / match_events with game='mafia'.

CREATE TABLE mafia_seats (
    match_id       BIGINT NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    seat           INT NOT NULL CHECK (seat >= 1 AND seat <= 12),
    agent_id       BIGINT NOT NULL REFERENCES agents(id),
    owner_user_id  BIGINT NOT NULL REFERENCES users(id),
    role           TEXT NOT NULL,  -- Mafia|Detective|Doctor|Sheriff|Villager
    team           TEXT NOT NULL,  -- town|mafia
    alive          BOOLEAN NOT NULL DEFAULT true,
    coins_delta    BIGINT,
    PRIMARY KEY (match_id, seat),
    UNIQUE (match_id, agent_id)
);
CREATE INDEX idx_mafia_seats_agent ON mafia_seats (agent_id);
CREATE INDEX idx_mafia_lobby ON matches (game, bid) WHERE status = 'waiting' AND game = 'mafia';
