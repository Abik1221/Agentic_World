# Tests

Two layers:

1. **Unit tests** live next to the code they test (Go idiom): `internal/**/X_test.go`.
   Pure logic, fakes for ports, no DB/Redis. Run with `make test`.
2. **Integration / e2e** live here under `tests/integration/` behind the
   `integration` build tag. They drive the real HTTP server against a live
   Postgres + Redis (and Stripe test mode / DevGateway). Run with:

   ```bash
   make compose-up                      # Postgres + Redis + migrations + server
   go test -tags=integration ./tests/integration/...
   ```

3. **Load / soak** — `deploy/loadtest/k6-match-loop.js` (k6).

> **Environment note:** this repo was authored without a Go toolchain in the loop,
> so `go test` has not been executed here. Tests are written to compile and assert
> against the documented contracts; run them locally to go green.

## Unit coverage map

| Area | Test | What it pins |
|---|---|---|
| Engine | `engine/goofspiel/*_test` | rules, determinism, commit–reveal |
| Replay | `replay/replay_test` | reconstruct + verify + hash |
| Auth | `auth/auth_test` | scope guard, JWT |
| Identity | `identity/keys_test` | key hash/format |
| Match | `match/service_test` | lifecycle, idempotency, timeout sweep |
| **Ledger** | `ledger/ledger_test` | balanced / unbalanced / idempotent post |
| **Wallet** | `wallet/wallet_test` | stake→escrow, settle split, tie, **7 limits** |
| **Payments** | `payments/payments_test` | HMAC verify, webhook idempotency, refund |
| Spectator | `spectator/hub_test` | drop-slow, redaction, resume |
| Rating | `rating/elo_test` | ELO math, upset reward |
| Profiles | `profiles/profiles_test` | win-rate, style |
| Clips | `clips/detect_test` | dramatic triggers |
| Antifraud | `antifraud/*_test` | collusion/timing, gate hold, dispute idempotency |
| Tournament | `tournament/tournament_test` | eligibility gate, idempotent payout |

## Money-engine coverage GAPS this folder is being built to close
- **End-to-end money flow** (register → buy → stake → lose/settle → reconcile): integration.
- **Withdrawal / cash-out** (coins → money via Stripe Connect): NOT YET BUILT — see
  `docs/coin-engine-critique.md`. Tests land with the feature.
- **Atomic staking** (no orphaned escrow on partial failure): unit + integration.
- **Reconciliation invariant** (`wallet.balance == Σ entries`, zero drift) after a
  full buy/stake/settle/refund cycle: integration.
