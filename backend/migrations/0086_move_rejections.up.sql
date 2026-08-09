-- agent_move_rejections: a seat submitted a move for this round and the platform REFUSED it.
--
-- WHY THIS EXISTS. tryExtend gives a seat more time when it is still answering /health, on the
-- reasoning that a responsive agent is thinking rather than gone. Completion binding broke that
-- inference: an agent whose every move contradicts its own model's output is refused each time
-- and answers /health perfectly well throughout, so it reads as "still thinking" until the
-- policy ceiling. At MaxExtensions=3 that is roughly two minutes a round, on a table an
-- opponent has staked real coins on.
--
-- A seat that ANSWERED and was refused is not waiting on a model. Extending there rewards the
-- behaviour and holds up the other player. An agent that wants to correct a bad submission can
-- resubmit inside the window it already has — the extension is not the retry mechanism.
--
-- WHY A TABLE RATHER THAN DERIVING IT. "Was a move rejected for this seat this round" is not
-- recoverable from anything already stored: a refused move never becomes a decision (the
-- decision log is written on the applied path), the match row carries only the current state,
-- and the liveness probe answers a different question. It also has to survive across instances,
-- so it cannot live in memory.
--
-- WHY (match, agent, round) AND NOT A COUNT. One refusal in a round is enough to answer the
-- question being asked. Counting attempts would invite tuning a threshold, and there is no
-- number of refusals that means "still thinking".
CREATE TABLE IF NOT EXISTS agent_move_rejections (
    match_id   text        NOT NULL,
    agent_id   bigint      NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    round      integer     NOT NULL,
    -- The rule that refused the move, so a developer disputing a lost round can be told which
    -- control fired rather than only that something did.
    reason     text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (match_id, agent_id, round)
);

-- The read is on the deadline-sweep path, once per unsealed seat per expiry, and supplies all
-- three key columns — so the primary key serves it and no extra index is warranted.
