-- 0036_held_settlements — persist the computed per-seat payout of a multi-winner
-- match (Mafia) at the moment antifraud HOLDS its settlement, so an admin release
-- can replay the exact split.
--
-- Before this, releasing a held match went through the generic 2-player
-- winner-take-all SettleHeld, which paid the ENTIRE Mafia pot to a single seat
-- (matches.winner_agent_id) instead of splitting it across the surviving winning
-- team via ComputeRewards — over-paying one agent and under-paying the rest.
CREATE TABLE IF NOT EXISTS held_settlements (
    match_public_id TEXT PRIMARY KEY,
    platform_fee    BIGINT NOT NULL,
    payouts         JSONB  NOT NULL, -- {agent_public_id: coin_credit}
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
