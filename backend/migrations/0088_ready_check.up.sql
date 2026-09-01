-- LOCK SAFETY. Both ALTERs below need ACCESS EXCLUSIVE on their table, which queues behind any
-- in-flight read and then blocks every query that arrives after it. That is not hypothetical
-- here: the model board fit scans match events for minutes at a time (measured at 217s), and
-- this migration was observed waiting on exactly that lock during startup, holding the whole
-- server behind it.
--
-- Adding a nullable column is cheap in Postgres 11+ (no table rewrite) — the risk is entirely
-- the WAIT, not the work. lock_timeout makes it fail fast and loudly instead of wedging the
-- database: a migration that errors is retried in seconds, a migration that blocks takes the
-- platform down with it.
SET lock_timeout = '5s';

-- ready_check: a matched table waits for every seat to say it is there BEFORE any money moves.
--
-- WHY THIS EXISTS. CreatePaired documents its own behaviour: it "escrows both stakes, deals the
-- match, and persists it active in one step — no waiting window". So the instant the matcher
-- pairs two agents, real coins are locked and a staked match is live. Two people lose by that:
-- a developer who typed a command in a terminal and never got the chance to open the match, and
-- an agent that was paired while its process was still starting and forfeits a stake it never
-- played for. Once a match is active a failing agent does not get its coins back, so the only
-- protection is to not take them until the seat has answered.
--
-- The rule these columns exist to enforce: NOTHING IS ESCROWED UNTIL EVERY SEAT IS READY. A seat
-- that never answers costs its owner time, not money.
--
-- WHY A NEW STATUS RATHER THAN REUSING 'waiting'. 'waiting' means an OPEN table in the lobby that
-- anyone may join, and it is indexed as exactly that (idx_matches_lobby, idx_mafia_lobby). A
-- paired table is closed — its seats are already decided — so listing it in the lobby would let a
-- third agent try to join a match that has no free seat. Separate status, separate index.
--
-- WHY PER-SEAT COLUMNS RATHER THAN A COUNT. "Who has not answered" is the question every branch
-- needs: the sweeper re-asks that seat, drops that seat, and requeues that agent. A count answers
-- none of them. ready_asks is on the seat for the same reason — a second ask is offered because
-- one missed ask is indistinguishable from a network blip, and the count of asks so far is what
-- separates "blipped" from "gone".
--
-- starts_at is an ABSOLUTE instant, not a countdown. A terminal and a browser each counting down
-- from ten drift apart within seconds and visibly disagree; both counting to the same timestamp
-- cannot. Every surface renders from this one value.

ALTER TABLE matches
    -- The instant the first turn begins. Set once every seat is ready and the stakes are
    -- escrowed; null before then. Null on an active match means it predates this migration.
    ADD COLUMN IF NOT EXISTS starts_at TIMESTAMPTZ;

ALTER TABLE match_players
    -- When this seat acknowledged. Null = not ready. A timestamp rather than a boolean so a
    -- late ack can be told from an early one when explaining why a table started when it did.
    ADD COLUMN IF NOT EXISTS ready_at      TIMESTAMPTZ,
    -- How many times this seat has been asked. 0 = never asked, which is NOT silence and must
    -- never be treated as a reason to drop someone.
    ADD COLUMN IF NOT EXISTS ready_asks    INT NOT NULL DEFAULT 0,
    -- When the outstanding ask was sent, so its window can be measured.
    ADD COLUMN IF NOT EXISTS ready_asked_at TIMESTAMPTZ;

-- The sweeper's only query: tables still collecting acknowledgements. Partial, like
-- idx_matches_sweep, because ready_check is a brief state and the index should not carry every
-- finished match in the table.
CREATE INDEX IF NOT EXISTS idx_matches_ready_check
    ON matches (id) WHERE status = 'ready_check';
