-- 0073_agent_match_decisions — the per-DECISION record a developer needs to improve
-- an agent, which the platform was measuring and then throwing away.
--
-- WHAT WAS MISSING. `agent_match_benchmark` (0051/0053/0056/0072) stores one row per
-- (match, agent): 240 decisions, 96% legal, 40s of latency, 12k tokens. That answers
-- "how did the agent do" and cannot answer the only question a developer debugging an
-- agent actually asks — "what did it do on round 7, why, and what did that cost".
--
-- The data existed the whole time. benchmark.SeatSummary.DecisionLog carries, per move:
-- round, action, outcome, latency, the agent's own rationale, and the token usage of
-- the call that produced it. It was emitted to Pyyol Lens and to nowhere else, so with
-- Lens unconfigured — the default — every per-round record was discarded at match end.
-- A schema-wide search for a column named `rationale` or `reasoning` returned zero.
--
-- The one partial exception proved the rule: Goofspiel and Monopoly relay a move's
-- rationale back through the chat path (Say(..., ChatKindRationale)) so it survives as
-- a match_event, while Mafia does not — so reasoning was visible in two arenas out of
-- three, by accident of an unrelated feature.
--
-- PRIVACY. A rationale is the agent's private reasoning about opponents. Every read
-- path over this table MUST be scoped to the caller's own agents; the devtrace service
-- gates on ownership before it queries. This table must never be joined into a public
-- surface (spectator, leaderboard, model board) — those show what an agent DID, never
-- what it was thinking.

BEGIN;

CREATE TABLE IF NOT EXISTS agent_match_decisions (
    -- match_id is the match's PUBLIC id as TEXT, matching agent_match_benchmark: the
    -- benchmark fact arrives from an event payload that carries the public id, and
    -- resolving it to matches.id on the write path would make the writer fail for a
    -- match row that has not landed yet.
    match_id   TEXT   NOT NULL,
    agent_id   BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    -- Position within this seat's decision log. Ordering key: `round` is NOT unique
    -- (Mafia takes several actions per day) and is 0 in arenas that do not number
    -- rounds, so it cannot carry the ordering by itself.
    seq        INT    NOT NULL,

    round      INT    NOT NULL DEFAULT 0,
    action     TEXT   NOT NULL DEFAULT '',
    outcome    TEXT   NOT NULL DEFAULT '',   -- ok|illegal|timeout|transport_error|…
    latency_ms BIGINT NOT NULL DEFAULT 0,

    -- The agent's own explanation of the move, as it returned it. Free text, bounded by
    -- the SDK's own cap on the decision log.
    rationale  TEXT   NOT NULL DEFAULT '',

    -- Which model produced THIS move. Per-decision rather than per-match because an
    -- agent may legitimately use a cheap model for routine turns and an expensive one
    -- for hard ones — and a developer tuning that split cannot see it in an average.
    provider   TEXT   NOT NULL DEFAULT '',
    model      TEXT   NOT NULL DEFAULT '',

    prompt_tokens     INT NOT NULL DEFAULT 0,
    completion_tokens INT NOT NULL DEFAULT 0,
    reasoning_tokens  INT NOT NULL DEFAULT 0,
    cached_tokens     INT NOT NULL DEFAULT 0,
    total_tokens      INT NOT NULL DEFAULT 0,
    estimated_cost    DOUBLE PRECISION NOT NULL DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (match_id, agent_id, seq)
);

-- The read path is always "this match, this agent, in order", which the primary key
-- already serves. This second index is for "my agent's recent decisions across
-- matches" — the feed view.
CREATE INDEX IF NOT EXISTS idx_agent_match_decisions_agent_recent
    ON agent_match_decisions (agent_id, created_at DESC);

COMMIT;
