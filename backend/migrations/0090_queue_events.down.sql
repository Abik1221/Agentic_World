-- Dropping this loses the queue history and nothing else: no matchmaking decision reads it,
-- which is the property that makes the table safe to add and safe to remove.
SET lock_timeout = '5s';

DROP TABLE IF EXISTS queue_events;
