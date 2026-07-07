-- Revert: return any reserved rows to waiting, then restore the 2-value constraint.
UPDATE matchmaking_queue SET status = 'waiting' WHERE status = 'claimed';
ALTER TABLE matchmaking_queue DROP CONSTRAINT matchmaking_queue_status_chk;
ALTER TABLE matchmaking_queue ADD CONSTRAINT matchmaking_queue_status_chk
    CHECK (status IN ('waiting', 'matched'));
