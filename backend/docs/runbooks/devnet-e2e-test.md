# Devnet end-to-end test — real agents, real stakes, real settlement

Goal: prove the whole money-and-matchmaking path on devnet with **real agents**, not the
sandbox. Two agents play Goofspiel, four play Mafia, and we watch the queue fill, the match
run in the UI, and the pool settle with the platform fee taken.

This is a **test plan you execute**, not an incident playbook. Work top to bottom; each
section ends with something you can check before moving on.

---

## 0. Two decisions before you create anything

Both of these waste hours if discovered halfway through.

### 0.1 The configured mint has no faucet

Production currently accepts:

```
spl_token = Fr8dGZ3MMwd3WsSYbdkxhAd4vkezTZfnQVn2aqXh5QaU
```

That mint exists **on devnet only** — good, devnet is what we want — but it is **not**
Circle's devnet USDC (`4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU`). It is a custom
token, so **no public faucet will fund it**. You can only mint it if you still hold its
mint authority.

Pick one before continuing:

- **Keep the custom mint** — you must have its mint authority keypair to fund test wallets
  (`spl-token mint <MINT> <AMOUNT> <RECIPIENT_ATA>`).
- **Switch to Circle devnet USDC** — set `SOLANA_USDC_MINT` to the Circle mint above and
  fund test wallets from Circle's devnet faucet. `cmd/devnet-pay` assumes this path.

There is no third option: a test wallet cannot pay a token nobody can issue.

### 0.2 Mafia needs FOUR agents, not twelve

```
MAFIA_MIN_SEATS   default 4    (no secret overrides it in production)
RAKE_PCT          default 5    (no secret overrides it in production)
```

The 12-seat table is **push-play**, the practice mode where house bots fill the seats. For
a real-agent test the minimum is **4**. Duplicate three extra agents, not eleven.

---

## 1. The three platform wallets

| Wallet | Config keys | Role |
|---|---|---|
| **Deposit** | `SOLANA_PLATFORM_OWNER`, `SOLANA_PLATFORM_ATA` | where user deposits land |
| **Hot (payout float)** | `SOLANA_HOT_WALLET_SECRET_ENC`, `SOLANA_PAYOUT_ATA` | signs withdrawals |
| **Cold** | `SOLANA_COLD_WALLET_ADDRESS` | sweep destination, **advisory only** |

> `*_ATA` values are **token accounts**, not wallet addresses. Pasting a wallet address into
> an ATA variable is the single most common setup mistake here — deposits then land
> somewhere the poller never looks, and nothing errors.

```bash
export MINT=<the mint you chose in 0.1>
export RPC=https://api.devnet.solana.com

# Deposit wallet
solana-keygen new -o deposit.json --no-bip39-passphrase
DEPOSIT_OWNER=$(solana-keygen pubkey deposit.json)
solana airdrop 2 "$DEPOSIT_OWNER" -u devnet
DEPOSIT_ATA=$(spl-token create-account "$MINT" --owner "$DEPOSIT_OWNER" \
  --fee-payer deposit.json -u devnet | awk '/Creating account/{print $3}')

# Hot wallet — signs payouts, so it holds a float and nothing more
solana-keygen new -o hot.json --no-bip39-passphrase
HOT_OWNER=$(solana-keygen pubkey hot.json)
solana airdrop 2 "$HOT_OWNER" -u devnet
PAYOUT_ATA=$(spl-token create-account "$MINT" --owner "$HOT_OWNER" \
  --fee-payer hot.json -u devnet | awk '/Creating account/{print $3}')

# Cold wallet — address only. Its key must NEVER reach config.
solana-keygen new -o cold.json --no-bip39-passphrase
COLD_ADDRESS=$(solana-keygen pubkey cold.json)
```

**Split the deposit and payout accounts** — do not point `SOLANA_PAYOUT_ATA` at the deposit
ATA. With one account, ordinary deposits push the signing wallet past `HOT_WALLET_CAP_CENTS`
and train you to ignore the one alert that bounds blast radius. Since you are creating these
fresh, split them now; it costs a manual cold→hot top-up when the float runs low.

**Never put the cold key in config.** Nothing in the server signs a sweep, deliberately —
the address is configured only so an alert can name the destination instead of you pasting
one mid-incident.

### Sealing the hot key

Do not set the private key as a plaintext env var. The repo has a tool:

```bash
# from backend/
SOLANA_HOT_WALLET_ENC_KEY=<master-key> \
  go run ./cmd/wallet-secret-encrypt <base58-hot-wallet-secret>
```

It prints `base64(secretbox ciphertext)` → `SOLANA_HOT_WALLET_SECRET_ENC`. The master goes
in `SOLANA_HOT_WALLET_ENC_KEY`. Keep the master out of shell history — read it from a file
or a secret store, and set both as **GitHub secrets**, never in the repo.

### Secrets to set

```
SOLANA_CLUSTER=devnet
SOLANA_RPC_URL=https://api.devnet.solana.com
SOLANA_USDC_MINT=<from 0.1>
SOLANA_PLATFORM_OWNER=<DEPOSIT_OWNER>
SOLANA_PLATFORM_ATA=<DEPOSIT_ATA>
SOLANA_PAYOUT_ATA=<PAYOUT_ATA>
SOLANA_COLD_WALLET_ADDRESS=<COLD_ADDRESS>
SOLANA_HOT_WALLET_SECRET_ENC=<sealed>
SOLANA_HOT_WALLET_ENC_KEY=<master>
```

**Check before moving on:**

```bash
curl -s https://api.pyyol.com/v1/deposits/config -H "authorization: Bearer $TOK"
```

`recipient` and `spl_token` must equal the values you just set. If they do not, the deploy
did not pick up the secrets — fix that before creating accounts.

---

## 2. Test accounts and agents

You need **two platform accounts** (yours plus one more), each with at least one agent.
Sign the second one up through the site like any developer would — that is part of what is
being tested.

| Game | Agents needed |
|---|---|
| Goofspiel | 2 |
| Mafia | 4 |

Duplicate the existing agent rather than writing new ones: same code, **different API key**
per agent, and spread across the two accounts so the queue is pairing genuinely different
owners.

> Two agents owned by the same account may be treated as the same side by anti-collusion
> checks. Spread them across both accounts, and if a pairing never forms, suspect this
> before suspecting the queue.

Each agent's payer wallet also needs a little SOL for fees. These are **separate** from the
three platform wallets in §1.

---

## 3. Fund the accounts

### 3.1 Get tokens into a test wallet

Circle mint → devnet faucet. Custom mint → `spl-token mint` with the mint authority.

### 3.2 Deposit into the platform

```bash
curl -s -X POST https://api.pyyol.com/v1/deposits \
  -H "authorization: Bearer $TOK" -H 'content-type: application/json' \
  -d '{"amount_usdc":10}'
```

The response carries `pay_url`, `reference`, `deposit_id`. Pay it from the terminal instead
of a phone wallet:

```bash
go run ./cmd/devnet-pay \
  -payer ~/devnet-payer.json \
  -rpc https://api.devnet.solana.com \
  -url '<pay_url from the response>'
```

`devnet-pay` attaches the Solana Pay `reference` pubkey as a read-only account. **That is
the whole point** — the backend detects deposits with `getSignaturesForAddress(reference)`,
so a transfer sent without it lands on-chain and is never credited. If you pay by hand and
the coins never arrive, this is why.

Poll `GET /v1/deposits/{id}` until `coins_credited` is non-zero.

### 3.3 The step everyone misses

Deposited coins land in the **user** wallet. Agents stake from an **agent** wallet. Move
them:

```bash
curl -s -X POST https://api.pyyol.com/v1/wallet/allocate \
  -H "authorization: Bearer $TOK" -H 'content-type: application/json' \
  -d '{"agent":"<agent_public_id>","amount":500,"idempotency_key":"alloc-1"}'
```

Skip this and the deposit looks successful, the balance looks correct, and the agent still
cannot stake. Do it for **every** agent that will play.

---

## 4. Goofspiel — the queue test

The point is the *waiting*, so start the agents deliberately apart.

1. Start agent **A**. Confirm it is queued and **waiting** — it must not error, and it must
   not start a match alone.
2. Leave it waiting long enough to be sure it is genuinely holding a slot.
3. Start agent **B**.
4. It must join the same table and the match must begin, since Goofspiel's minimum is 2.
5. Watch it live in the UI — the match page shows the same viewer used for every table.

**Record:** how long B took to be paired, and whether the UI showed the match without a
manual refresh.

---

## 5. Mafia — the same test at four seats

Identical shape, minimum **4**. Start the agents **one at a time**, confirming after each
that the table is still waiting and the seat count climbed. On the fourth, the match must
start.

This is the part most likely to expose a real bug: a queue that starts early is as wrong as
one that never starts.

---

## 6. Settlement — what to verify

Follow the ledger; each step is named:

| Kind | Meaning |
|---|---|
| `stake` | agent → escrow at match start |
| `settle` | escrow → winner, **minus rake** |
| `refund` | escrow → players on abort |

At `RAKE_PCT=5`, for a two-sided pool:

```
pool          = bid × 2
winner credit = pool − 5%
platform fee  = pool × 5%
```

Confirm all three:

1. Both agents show a `stake` debit at match start.
2. The winner's `settle` credit equals the pool minus 5%.
3. The 5% actually lands in the platform wallet — not merely subtracted from the winner.

Point 3 is the one worth being strict about: a fee that is deducted but never banked looks
correct from the player's side and quietly loses the platform money.

---

## 7. If the queue misbehaves

To separate *matchmaking* faults from *settlement* faults, run the same test through a
**private room** (dashboard → Games → Play a friend) instead of the queue.

Rooms take the identical settlement path — from `CreateRoom` in `internal/match/service.go`:

> *"The room does not skip a check, does not escape the rake, and does not get a different
> settlement path. It is the open lobby minus the listing."*

So a room verifies staking, settlement and the fee deterministically with both sides under
your control. If money is correct via a room but the queue never pairs, the fault is in
matchmaking, not settlement.

---

## 8. Known-suspect areas — check these first when something breaks

Ordered by how likely they are to bite:

1. **Mint/faucet mismatch** (§0.1) — a wallet cannot pay a token nobody can issue.
2. **ATA vs wallet address** (§1) — deposits land where the poller never looks.
3. **Missing allocation** (§3.3) — funded user, unfundable agent.
4. **Deposits returning a generic error.** As of 2026-09-03 the deposit path returns
   `An unexpected error occurred.` (a 500) in production and the cause is **not yet
   identified**. The config endpoint and deposit list both respond correctly, and the admin
   gates (maintenance, deposits-disabled, frozen wallet) all return specific 4xx messages —
   so the failure is something further down. If it recurs here, that is the moment to grab
   the server logs, because it will be reproducible on demand.

Report anything that fails with: the endpoint, the exact response body, and the ledger rows
around it. "Deposit failed" is not actionable; the 500 above is exactly why.
