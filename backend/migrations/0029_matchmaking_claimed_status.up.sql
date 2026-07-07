-- 0029_matchmaking_claimed_status — add an intermediate 'claimed' state so the
-- matcher can atomically reserve a pair BEFORE escrowing stakes. This closes a
-- double-seat/double-stake race: previously CreatePaired (which escrows both
-- stakes) ran before an unconditional MarkMatched, so a transient MarkMatched
-- failure — or a second matcher instance reading the same waiting snapshot —
-- could re-pair the same two agents and escrow their stakes twice.
ALTER TABLE matchmaking_queue DROP CONSTRAINT matchmaking_queue_status_chk;
ALTER TABLE matchmaking_queue ADD CONSTRAINT matchmaking_queue_status_chk
    CHECK (status IN ('waiting', 'claimed', 'matched'));
