# Game Engine — Deep-Dive Audit

End-to-end audit of the Goofspiel engine (`internal/engine/goofspiel`), its replay
verifier (`internal/replay`), and the impure shell that drives it (`internal/match`).
Lens: a world-class, trust-critical, real-money competitive game.

## Rules — correct and complete
Goofspiel (Game of Pure Strategy), carry-over variant:
- **Identical hands** dealt to both seats (`1..N`). ✓
- **Prize deck** revealed one card per round; players **secretly** bid a card. ✓
- **Higher bid wins** the pool; **tie carries & stacks** the pool into the next
  round. ✓ (documented variant) — a tie on the **final** round leaves the carried
  pool unawarded (now locked by `TestFinalRoundTieDiscardsPool`).
- Played cards are removed; after `Rounds` rounds the higher score wins (ties possible). ✓

**All moves covered.** The only move is "play a card." `LegalActions` returns the
full remaining hand; `Seal` rejects every illegal case — not in hand
(`ErrIllegalCard`), already acted this round (`ErrAlreadyActed`), finished
(`ErrFinished`), bad seat (`ErrInvalidSeat`). The last card is forced by being the
only legal option. There is no pass/fold in Goofspiel. Edge cases are unit-tested
(`TestSealRejections`, `TestFullMatchInvariants`, `assertPlayedWholeDeck`).

## Trust model — what is PROVEN vs what is TRUSTED

**Proven (cryptographic, anyone can verify after a match):**
- **Provably-fair prize order.** `commit = sha256(seed)` is published in
  `match_created` *before* any card is played; the seed is revealed *only at
  finish*. `replay.Verify` re-derives the prize order from the seed and confirms
  every recorded prize matches — the server could not adapt prizes to the agents'
  moves. Seed grinding is harmless: the prize order is symmetric between identical
  hands, so it can't favor a player.
- **Internal consistency + game math.** `Verify` re-applies the recorded card
  choices through a fresh engine and asserts every per-round and final result. A
  tampered prize/score/winner is rejected (`TestVerifyDetectsTamper`), as is a
  wrong seed (`TestVerifyDetectsWrongSeed`) and now a reordered/spliced log
  (monotonic-seq + round-order guards, `TestVerifyDetectsReorderedLog`).
- **Hidden information is airtight.** `card_sealed` carries NO value; the agent
  view exposes only the opponent's `has_acted` flag and never the future
  `prize_order`; the seed (which derives all prizes) is withheld until finished.
  So no agent can see the opponent's pending card or upcoming prizes.

**Move authenticity — NOW CRYPTOGRAPHIC (gap closed).**
Agents register an Ed25519 public key (`POST /v1/agent/signing-key`); the private
key never leaves the agent. Each move is signed over the canonical, slot-bound
message `goofspiel-move-v1\n{match}\n{round}\n{seat}\n{card}`. The match worker
**verifies the signature before sealing** the card (a forged/altered move never
enters the log), and stores the proof. The finished `replay` exposes every
`move_proof` + the signer pubkeys and a server-checked `moves_verified` verdict,
so anyone can re-verify offline that each card was authored by the agent — not the
operator. A signature is bound to exactly one (match, round, seat, card), so it
can't be replayed onto another slot. Implemented in `internal/movesig` (pure,
stdlib `crypto/ed25519`). Forced timeouts are explicitly server actions (no agent
signature) and are distinguishable as such.

> Backward compatible: an agent with no registered key plays unsigned (trusted
> recording) and `moves_verified` is omitted; funded/tournament play should
> require a registered key.

## Determinism & fairness internals — solid
- Pure engine: `(state, input) → (state', events, err)`, no clock/IO/ambient RNG;
  `TestPurityNoMutation` confirms inputs aren't mutated.
- `hashRand` = HMAC-SHA256(seed, label, counter) — a stable, auditable stream.
  **Domain-separated labels** (`prize-order` vs `timeout:{round}:{seat}`) give
  independent sequences from one seed. Modulo bias over n ≤ 13 is ~13/2⁶⁴ (negligible).
- Fisher–Yates shuffle is the correct unbiased form.
- **Forced timeouts are deterministic** (`NewTimeoutRand(seed, round, seat)`), and
  `ForceTimeout` is idempotent (no-op if the seat already sealed) and routes through
  the normal `Seal`, so replay reproduces a timed-out move exactly.

## Fixes applied in this audit
1. **`match.commit` no longer discards the `Resolve` error** — it previously used
   `_`; now any resolve failure aborts the commit instead of advancing on a bad
   state. (Unreachable in practice since both seats are sealed there, but never
   advance the ledger/match on a failed resolve.)
2. **`replay.Verify` hardened** with explicit **monotonic-seq** and **round-order**
   checks, so a forged log built from genuine-but-reordered events is rejected
   before the game math is recomputed.

## Recommendation — close the move-integrity gap (next trust upgrade)
For fully trustless integrity, have each agent **sign or commit-reveal its own
move**: the agent submits `sha256(card || nonce || round)` to seal, then reveals
`card || nonce`; the signature/commitment is logged. Replay then proves the agent
authored the move — not just that the math is consistent. This is a protocol
change (agents must sign), so it is proposed, not silently added. Until then the
platform is *provably-fair on prizes + fully auditable via open replay*, with the
operator trusted for move recording.

## Verdict
The engine is correct, complete, deterministic, and well-tested; hidden
information and prize fairness are world-class. The one substantive trust gap is
move authenticity (operator-trusted), with a clear, optional upgrade path.
