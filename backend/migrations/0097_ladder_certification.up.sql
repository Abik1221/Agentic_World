-- 0097_ladder_certification — the certified exploitability ladder (Pyyol Lab).
--
-- WHAT THIS MEASURES. Every competing LLM game benchmark reports win rate against a pool of
-- other models: pool-relative, farmable by choosing opponents, and meaningless to compare
-- across time or across labs. Exploitability is absolute. For a two-player zero-sum game,
-- eps(sigma) = max over deviations of u(sigma', sigma) - v. Zero means unexploitable, the
-- opponent pool does not appear in the formula, and the only way to improve the number is to
-- play closer to equilibrium.
--
-- Goofspiel with equal hands is SYMMETRIC, so its value is exactly 0 and
-- eps(sigma) = u(BR(sigma), sigma) — no equilibrium solve, no linear program. See
-- internal/gops. The protocol and its proof live in internal/exploit; the state machine in
-- internal/ladder.
--
-- LAB TRACK ONLY. Every match a run drives is zero-stake and unrated. This schema deliberately
-- carries no money column and references no wallet: a certification run is an experiment, and
-- the moment a measurement can move coins it acquires an incentive to be wrong.

-- ── The pinned spec ─────────────────────────────────────────────────────────────────────
--
-- APPEND-ONLY BY POLICY, and the reason is a scar. `PutConfig` upserts already-published
-- P-Index parameters in place (store/pindex_repo.go), so "v3" stopped meaning what it meant
-- when developers were scored under it. A certificate cites a spec version, so that version
-- has to mean one thing forever. The trigger below enforces what a comment could not.
CREATE TABLE IF NOT EXISTS lab_ladder_spec (
    version           INT PRIMARY KEY,
    n                 INT         NOT NULL CHECK (n >= 2 AND n <= 8),
    -- The SET of boards a run cycles through, as JSONB (an array of permutations). More
    -- than one is what makes the metric unfarmable: a single fixed board is memorisable and
    -- a lookup table plays it perfectly. JSONB rather than INT[][] because Postgres requires
    -- rectangular multidimensional arrays and pgx maps [][]int cleanly through JSON.
    prize_orders      JSONB       NOT NULL,
    tie_rule          TEXT        NOT NULL DEFAULT 'carry' CHECK (tie_rule = 'carry'),
    alpha             DOUBLE PRECISION NOT NULL CHECK (alpha >= 0),
    delta             DOUBLE PRECISION NOT NULL CHECK (delta > 0 AND delta < 1),
    -- Who the agent plays during Phase A. Part of the spec identity: the reference decides
    -- which information sets the fit ever sees, so two runs with different references did
    -- not run the same experiment.
    -- How many bootstrap best responses the prober mixes over. In the identity because a
    -- run measured against one prober is not the same experiment as one measured against
    -- eight, and because 1 reinstates the memorisation vulnerability.
    prober_mixture    INT         NOT NULL DEFAULT 8 CHECK (prober_mixture >= 1),
    -- Phase B stops when the bound's slack falls below this fraction of the payoff range.
    -- Zero spends the whole budget. Persisted because it is part of the experiment: a run
    -- measured to 5% precision is not the same run as one that exhausted its cap.
    target_precision  DOUBLE PRECISION NOT NULL DEFAULT 0.05
                                  CHECK (target_precision >= 0 AND target_precision < 1),
    reference_opponent TEXT       NOT NULL DEFAULT 'uniform'
                                  CHECK (reference_opponent IN ('uniform','nearest_pool')),
    phase_a_matches   INT         NOT NULL CHECK (phase_a_matches >= 1),
    first_checkpoint  INT         NOT NULL CHECK (first_checkpoint >= 2),
    max_phase_b       INT         NOT NULL,
    budget_ms         BIGINT      NOT NULL CHECK (budget_ms > 0),
    per_move_ms       BIGINT      NOT NULL CHECK (per_move_ms > 0),
    increment_ms      BIGINT      NOT NULL CHECK (increment_ms >= 0),
    grace_ms          BIGINT      NOT NULL CHECK (grace_ms >= 0),
    -- Canonical SHA-256 over the values, computed in Go (ladder.Spec.Hash). Stored so a
    -- reader can verify the row was not edited without recomputing from the code.
    spec_hash         TEXT        NOT NULL,
    active            BOOLEAN     NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A run that cannot reach its first look produces an uninformative certificate for a
    -- reason that has nothing to do with the agent.
    CONSTRAINT lab_spec_budget_reaches_a_look CHECK (max_phase_b >= first_checkpoint)
);

COMMENT ON TABLE lab_ladder_spec IS
    'Immutable definitions of the certified exploitability ladder. Append-only: a published '
    'certificate cites a version, so that version must mean one thing forever.';

CREATE OR REPLACE FUNCTION lab_ladder_spec_is_immutable() RETURNS trigger AS $$
BEGIN
    -- `active` is the one mutable column: retiring a spec must stay possible, and it changes
    -- which spec NEW runs use without changing what any existing certificate measured.
    IF (NEW.version, NEW.n, NEW.prize_order, NEW.tie_rule, NEW.alpha, NEW.delta,
        NEW.prober_mixture, NEW.target_precision, NEW.reference_opponent, NEW.phase_a_matches, NEW.first_checkpoint, NEW.max_phase_b,
        NEW.budget_ms, NEW.per_move_ms, NEW.increment_ms, NEW.grace_ms, NEW.spec_hash)
       IS DISTINCT FROM
       (OLD.version, OLD.n, OLD.prize_order, OLD.tie_rule, OLD.alpha, OLD.delta,
        OLD.prober_mixture, OLD.target_precision, OLD.reference_opponent, OLD.phase_a_matches, OLD.first_checkpoint, OLD.max_phase_b,
        OLD.budget_ms, OLD.per_move_ms, OLD.increment_ms, OLD.grace_ms, OLD.spec_hash)
    THEN
        RAISE EXCEPTION 'lab_ladder_spec v% is immutable; publish a new version instead',
            OLD.version;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS lab_ladder_spec_immutable ON lab_ladder_spec;
CREATE TRIGGER lab_ladder_spec_immutable
    BEFORE UPDATE ON lab_ladder_spec
    FOR EACH ROW EXECUTE FUNCTION lab_ladder_spec_is_immutable();

-- Only one spec may be active at a time, so "the current ladder" is never ambiguous.
CREATE UNIQUE INDEX IF NOT EXISTS lab_ladder_spec_one_active
    ON lab_ladder_spec ((true)) WHERE active;

-- ── One agent's attempt at one spec ─────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS lab_cert_run (
    id            BIGSERIAL PRIMARY KEY,
    agent_id      BIGINT      NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    spec_version  INT         NOT NULL REFERENCES lab_ladder_spec(version),
    phase         TEXT        NOT NULL DEFAULT 'fit'
                              CHECK (phase IN ('fit','certify','done','abandoned')),
    fit_done      INT         NOT NULL DEFAULT 0,
    certify_done  INT         NOT NULL DEFAULT 0,
    -- Reconstruction census, accumulated across Phase A batches. Published on the
    -- certificate: a bound computed over a heavily filtered sample is a bound about a
    -- different population than the one it names.
    fit_kept      INT         NOT NULL DEFAULT 0 CHECK (fit_kept >= 0),
    fit_offered   INT         NOT NULL DEFAULT 0 CHECK (fit_offered >= fit_kept),
    -- Set exactly once, at the fit->certify transition. NULL while fitting.
    prober_digest TEXT,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A run past the fit phase without a pinned prober was measured against a strategy
    -- nobody recorded, and its certificate could never be checked.
    CONSTRAINT lab_run_prober_pinned_before_certify
        CHECK (phase = 'fit' OR phase = 'abandoned' OR prober_digest IS NOT NULL),
    -- ...and a run still fitting that already carries one has been rewound, which would fold
    -- Phase B play into the fit and void the sample split the bound depends on.
    CONSTRAINT lab_run_no_prober_while_fitting
        CHECK (phase <> 'fit' OR prober_digest IS NULL)
);

-- One OPEN run per (agent, spec). Two concurrent runs would interleave their Phase B matches
-- into each other's payoff stream and both bounds would be about a mixture.
CREATE UNIQUE INDEX IF NOT EXISTS lab_cert_run_one_open
    ON lab_cert_run (agent_id, spec_version) WHERE phase IN ('fit','certify');

CREATE INDEX IF NOT EXISTS lab_cert_run_open ON lab_cert_run (phase, updated_at)
    WHERE phase IN ('fit','certify');

-- ── Phase A, aggregated ─────────────────────────────────────────────────────────────────
--
-- Counts per (node, card), NOT raw per-decision rows. The fit/certify split is BY PHASE and
-- declared before any match is played, so aggregation loses nothing an auditor needs, and
-- storage becomes O(reachable nodes) — bounded by the spec — rather than O(decisions), which
-- grows without limit. The node is (my hand, opponent hand, carried pot); under the open-prize
-- ladder that triple IS the whole information set.
CREATE TABLE IF NOT EXISTS lab_fit_counts (
    run_id     BIGINT NOT NULL REFERENCES lab_cert_run(id) ON DELETE CASCADE,
    -- Which prize order this tally came from. Part of the key because two orders are two
    -- DIFFERENT games: the same (hand, hand, carry) triple means something else under
    -- 1,2,3,4,5 than under 5,4,3,2,1, and merging them would fit a policy to a game nobody
    -- played.
    prize_order INT   NOT NULL DEFAULT 0 CHECK (prize_order >= 0),
    node_me    INT    NOT NULL,
    node_opp   INT    NOT NULL,
    node_carry INT    NOT NULL,
    card       INT    NOT NULL,
    n          INT    NOT NULL DEFAULT 0 CHECK (n >= 0),
    PRIMARY KEY (run_id, prize_order, node_me, node_opp, node_carry, card)
);

-- ── Phase B, one row per match ──────────────────────────────────────────────────────────
--
-- Per MATCH, not per round: rounds within a match are dependent, and a bound that treated
-- them as independent would be too tight by roughly sqrt(rounds).
--
-- Keyed on match_id so re-driving a finished match after a crash cannot double-count a payoff.
-- seq preserves play order, which the sequential bound needs: it is read at a PREFIX, so
-- reordering would certify a different sample than the one the stopping decision was made on.
CREATE TABLE IF NOT EXISTS lab_certify_payoff (
    run_id     BIGINT           NOT NULL REFERENCES lab_cert_run(id) ON DELETE CASCADE,
    match_id   TEXT             NOT NULL,
    seq        INT              NOT NULL,
    payoff     DOUBLE PRECISION NOT NULL,
    created_at TIMESTAMPTZ      NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, match_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS lab_certify_payoff_seq
    ON lab_certify_payoff (run_id, seq);

-- ── The published certificate ───────────────────────────────────────────────────────────
--
-- spec_hash and prober_digest travel WITH the numbers. A bound without them names a
-- measurement nobody can reconstruct, and the entire argument for this ladder over a win-rate
-- leaderboard is that a stranger can check it.
CREATE TABLE IF NOT EXISTS lab_certificate (
    run_id        BIGINT PRIMARY KEY REFERENCES lab_cert_run(id) ON DELETE CASCADE,
    agent_id      BIGINT           NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    spec_version  INT              NOT NULL REFERENCES lab_ladder_spec(version),
    spec_hash     TEXT             NOT NULL,
    prober_digest TEXT             NOT NULL,
    games         INT              NOT NULL,
    mean_payoff   DOUBLE PRECISION NOT NULL,
    std_dev       DOUBLE PRECISION NOT NULL,
    -- The published claim: eps >= lower_bound with probability >= 1 - delta.
    lower_bound   DOUBLE PRECISION NOT NULL,
    delta         DOUBLE PRECISION NOT NULL,
    -- FALSE means the bound collapsed to zero. Published so a reader never mistakes an
    -- inconclusive run for "provably unexploitable" — the same reason internal/deception
    -- refuses to print a point estimate without its interval.
    informative   BOOLEAN          NOT NULL,
    stop_reason   TEXT             NOT NULL
                                   CHECK (stop_reason IN ('precision_reached','budget_exhausted')),
    -- Provenance a reader needs to compare two certificates fairly.
    fit_matches   INT              NOT NULL,
    kept_fraction DOUBLE PRECISION NOT NULL,
    computed_at   TIMESTAMPTZ      NOT NULL DEFAULT now()
);

-- The published bundle series, chained. A bundle commits to its predecessor's hash, so
-- dropping or altering an earlier certificate breaks every later link — the property the
-- platform's event log lacks, where per-message signatures leave deletion undetectable.
CREATE TABLE IF NOT EXISTS lab_bundle_chain (
    run_id      BIGINT PRIMARY KEY REFERENCES lab_cert_run(id) ON DELETE CASCADE,
    seq         BIGSERIAL,
    prev_hash   TEXT NOT NULL,
    hash        TEXT NOT NULL UNIQUE,
    issued_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One head, found by the highest seq. seq is a sequence rather than a timestamp because two
-- bundles issued in the same millisecond must still have a total order or the chain forks.
CREATE INDEX IF NOT EXISTS lab_bundle_chain_head ON lab_bundle_chain (seq DESC);

CREATE INDEX IF NOT EXISTS lab_certificate_board
    ON lab_certificate (spec_version, lower_bound DESC) WHERE informative;

COMMENT ON COLUMN lab_certificate.lower_bound IS
    'Certified claim: the agent is AT LEAST this exploitable, with probability >= 1-delta. '
    'A LOWER value is better. Conservative by construction — see internal/exploit.';
