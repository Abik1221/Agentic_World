-- 0012_move_signing — per-move authenticity. An agent registers an Ed25519 public
-- key and signs every move; the signature is stored so a replay proves the AGENT
-- authored each card (not just that the math is consistent). See docs/game-engine-audit.md.

ALTER TABLE agents ADD COLUMN signing_pubkey TEXT;  -- base64 Ed25519 public key (nullable until registered)

CREATE TABLE move_signatures (
    match_id   BIGINT NOT NULL REFERENCES matches(id),
    round      INT    NOT NULL,
    seat       INT    NOT NULL,
    card       INT    NOT NULL,
    signature  TEXT   NOT NULL,   -- base64 Ed25519 signature over (match, round, seat, card)
    pubkey     TEXT   NOT NULL,   -- the signer's pubkey, captured at submit (replay is self-contained)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (match_id, round, seat)
);
