-- Platform liveness heartbeat, so an OUTAGE can be told apart from an agent quitting.
--
-- Why this exists: a missed turn forfeits a staked match (the engine plays a fallback
-- move for the absent seat and it loses on merit, stake included). That is correct
-- when an agent quits or crashes — but identical on the wire when WE are the ones who
-- went away. Without a record of our own downtime, a five-minute outage would let the
-- recovery sweep force-timeout every in-flight match at once and silently confiscate
-- stakes from players who did nothing wrong.
--
-- Every instance heartbeats into a single row. On boot we read the previous beat: a
-- gap larger than the configured threshold means nobody was serving during it, so
-- deadlines that lapsed inside that window are unattributable and get grace instead
-- of a forfeit.
--
-- Deliberately ONE row (id = TRUE): this is a liveness signal, not a fleet registry.
-- Any instance beating is enough to prove the platform was up.
CREATE TABLE IF NOT EXISTS platform_liveness (
    id      BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    beat_at TIMESTAMPTZ NOT NULL
);

INSERT INTO platform_liveness (id, beat_at) VALUES (TRUE, now())
ON CONFLICT (id) DO NOTHING;

-- Audit trail of detected outages. Written once per boot when a gap is found, so an
-- operator can see exactly which window was graced and reconcile disputes later.
-- This is evidence, not control flow — grace is decided from the heartbeat gap.
CREATE TABLE IF NOT EXISTS platform_outages (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    started_at   TIMESTAMPTZ NOT NULL,  -- last heartbeat before the gap
    detected_at  TIMESTAMPTZ NOT NULL,  -- boot that noticed it
    grace_until  TIMESTAMPTZ NOT NULL,  -- forfeits suppressed until here
    gap_seconds  BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_platform_outages_detected ON platform_outages (detected_at DESC);
