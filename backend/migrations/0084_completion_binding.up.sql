-- Completion binding: what the MODEL answered, recorded independently of what the agent submits.
--
-- WHY THE EXISTING TABLE GAINS COLUMNS RATHER THAN A NEW TABLE BEING ADDED. A row in
-- agent_match_bound_decisions already means "a verified model call was made for this exact turn".
-- The move that call produced is a property OF that same fact, not a separate one, and splitting
-- them would create the possibility of a bound decision whose move row is missing or disagrees —
-- two records of one event that can drift, with nothing to say which is right.
--
-- ALL THREE COLUMNS ARE NULLABLE, and that is the design rather than laziness. A turn is bound
-- WITHOUT a move whenever the completion carried no structured move tool call: an agent that has
-- not adopted the tool contract, a provider shape we could not parse, a response too large to
-- capture. Those are all "the platform has nothing to say about this move", which must remain
-- indistinguishable from today's behaviour at match time. Only a PRESENT extracted_move that
-- DISAGREES with the submitted move may ever reject a turn — absence never does. Defaulting these
-- to '' instead would erase that distinction and turn every legacy row into a claim that the model
-- produced the empty move.
ALTER TABLE agent_match_bound_decisions
    -- The canonical move read out of the completion's tool call (internal/movebind), e.g.
    -- 'card:7', 'kill:3', 'bid:150'. Deliberately NOT the full signed form the move signature
    -- covers: that includes server-known state (Mafia's phase, Monopoly's seq) which the model
    -- never chose, and binding it would reject honest turns over a field the model had no say in.
    ADD COLUMN IF NOT EXISTS extracted_move  text,
    -- SHA-256 of the exact completion bytes the gateway observed. Pins the evidence: a dispute
    -- about a rejected turn is settled by re-deriving the move from the response this hashes,
    -- rather than by trusting the extracted_move column that is itself under question.
    ADD COLUMN IF NOT EXISTS completion_hash text,
    -- HMAC over (agent, match, round, completion_hash, extracted_move) under the turn-proof
    -- secret. Makes the row TAMPER-EVIDENT: anything with database write access can change
    -- extracted_move, but not to a value that still verifies. Without it, the strongest control
    -- on the platform would rest on an ordinary mutable column.
    ADD COLUMN IF NOT EXISTS bind_receipt    text;

-- The match-time lookup: "what move did the model produce for this turn". Runs on the request
-- path of every move in a bound match, so it is indexed rather than left to the primary key's
-- prefix — the PK is (match_id, agent_id, round) and this query supplies all three, but only
-- rows that actually carry a move are ever interesting, and the partial index keeps the read
-- proportional to the bound-with-move population rather than to every bound turn ever played.
CREATE INDEX IF NOT EXISTS idx_bound_decisions_move
    ON agent_match_bound_decisions (match_id, agent_id, round)
    WHERE extracted_move IS NOT NULL;
