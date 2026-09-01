-- LOCK SAFETY: creating a new table takes no lock on anything existing, so unlike 0088/0089
-- this cannot queue behind a long read. lock_timeout is kept anyway, cheaply, so a migration
-- in this file set can never be the thing that wedges the database.
SET lock_timeout = '5s';

-- queue_events: the append-only history of what happened to a queued agent.
--
-- WHY THIS EXISTS. matchmaking_queue and group_queue are LIVE tables, PRIMARY KEY (agent_id) —
-- one row per agent, deleted the moment a match finalizes (and by SweepOrphanedEntries for the
-- commit race). That is the right shape for matchmaking and the wrong shape for answering the
-- questions anyone actually asks when the arena feels broken:
--
--   "how many agents sat in the queue and never got a game, and for how long?"
--   "how many went unreachable after their developer started them?"
--   "where do agents get stuck?"
--
-- Every one of those needs the entry AFTER it stopped existing. The live table deletes the
-- evidence at exactly the moment it becomes interesting, so no query against it can answer
-- them — only a count of who is waiting right now, which is the one thing already visible.
--
-- WHY APPEND-ONLY. A queue entry's life is a sequence, not a state: enqueued → matched, or
-- enqueued → asked → dropped → requeued → matched. Collapsing that into columns on the live
-- row loses the ordering, and the ordering IS the funnel. Append-only also means this can
-- never corrupt matchmaking: nothing reads it to make a decision.
--
-- WHY NOT DERIVE IT FROM match rows. A match row proves a pairing happened. It cannot show an
-- agent that waited twenty minutes and never paired, which is the case with no artefact
-- anywhere else in the schema — and the case a developer is most likely to complain about.
CREATE TABLE IF NOT EXISTS queue_events (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id       BIGINT      NOT NULL REFERENCES agents(id),
    -- Denormalised on purpose: per-user reporting is the point, and an agent can be deleted
    -- or transferred while its history stays meaningful. Nullable so a missing owner never
    -- blocks recording the event — losing the row would lose the evidence entirely.
    owner_user_id  BIGINT      REFERENCES users(id),
    game           TEXT        NOT NULL,
    -- Which queue: '2p' (matchmaking_queue) or 'group' (group_queue). Not a FK to anything;
    -- it is a fact about where the agent was waiting.
    queue          TEXT        NOT NULL,
    bid            BIGINT      NOT NULL DEFAULT 0,
    -- What happened. Deliberately a short closed set, checked below: an open text column
    -- would drift into a dozen spellings of the same event and the funnel would silently
    -- stop adding up.
    --
    --   enqueued    the agent joined the queue
    --   matched     it was paired into a match (match_public_id set)
    --   ready_asked the ready check asked this seat to confirm
    --   ready_ok    the seat confirmed
    --   dropped     the seat never answered and was removed (no stake was taken)
    --   requeued    a dropped seat was put back in the queue
    --   left        the agent left without matching (cancelled, swept, or disconnected)
    kind           TEXT        NOT NULL,
    match_public_id TEXT,
    -- Free-form detail for the WHY, e.g. 'no_answer', 'orphan_sweep', 'cancelled'. Never
    -- parsed for logic; it exists so a human reading the funnel can tell two identical-looking
    -- drops apart.
    reason         TEXT        NOT NULL DEFAULT '',
    -- How long this agent had been waiting when the event happened, in ms.
    --
    -- WRITTEN AT EVENT TIME, not computed later from the previous row's timestamp. A requeued
    -- agent has several enqueues, so "the previous enqueued row" is ambiguous, and computing
    -- backwards would attribute one wait to the wrong attempt. The writer knows the enqueue
    -- instant it is measuring from; nothing downstream has to guess.
    waited_ms      BIGINT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT queue_events_kind_chk CHECK (kind = ANY (ARRAY[
        'enqueued','matched','ready_asked','ready_ok','dropped','requeued','left'
    ])),
    CONSTRAINT queue_events_queue_chk CHECK (queue = ANY (ARRAY['2p','group']))
);

-- The funnel and the never-matched query both scan by time, so time leads.
CREATE INDEX IF NOT EXISTS idx_queue_events_time ON queue_events (created_at DESC);
-- Per-user detail: "show me everything that happened to this developer's agents".
CREATE INDEX IF NOT EXISTS idx_queue_events_owner ON queue_events (owner_user_id, created_at DESC);
-- Per-agent replay of one entry's life, for explaining a single complaint.
CREATE INDEX IF NOT EXISTS idx_queue_events_agent ON queue_events (agent_id, created_at DESC);
