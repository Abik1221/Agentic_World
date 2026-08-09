# Pyyol — working notes for Claude

Pyyol is an arena where developers' LLM agents play staked games (Goofspiel, Mafia, Monopoly)
for real coins. Its central claim is **"real LLM agents playing for real stakes."** Almost every
control in this repo exists to make that claim true rather than merely stated.

## Layout

| path | what |
|---|---|
| `backend/` | Go arena server (chi, pgx). Migrations in `backend/migrations/`. |
| `sdk/python/`, `sdk/js/` | Developer SDKs. Must stay behaviourally identical — see Conformance. |
| `sdk/conformance/` | Shared JSON fixtures read by Go, Python AND JS test suites. |
| `tracing/` | Pyyol Lens: separate observability stack (ClickHouse, NATS, MinIO, Postgres). **Its own go.mod requiring Go 1.26.1** — the arena backend is 1.25. |

## Toolchain — Docker only

There is no local Go, and `make build` will fail. Always build in a container:

```bash
# arena backend (Go 1.25). Mount the REPO ROOT, not backend/ —
# the conformance tests read ../../../sdk/conformance/.
cd Agentic_World && docker run --rm -v "$PWD":/r \
  -v pyyol-gocache:/root/.cache/go-build -v pyyol-gomod:/go/pkg/mod \
  -w /r/backend golang:1.25-alpine sh -c "go build ./... && go test ./internal/..."

# tracing backend (Go 1.26 — a 1.25 image fails with a go.mod toolchain error)
cd Agentic_World/tracing && docker run --rm -v "$PWD":/r \
  -v pyyol-gocache26:/root/.cache/go-build -v pyyol-gomod26:/go/pkg/mod \
  -w /r/backend golang:1.26-alpine sh -c "go build ./..."

# SDKs
docker run --rm -v /path/to/Agentic_World:/r -w /r/sdk/python python:3.12-slim \
  sh -c "pip install -q -e . && pip install -q pytest && python -m pytest tests/ -q"
docker run --rm -v /path/to/Agentic_World:/r -w /r/sdk/js node:22-alpine \
  sh -c "npm ci --silent && npm test && npm run lint"
```

Two traps that cost real time:
- **`cmd && echo OK` after a pipe reports success even when the build failed** — the pipeline's
  exit status is the last stage's. Use `cmd >/tmp/b.log 2>&1; echo EXIT=$?`.
- Mounting `backend/` and then using `-w /r/backend` silently gives "directory not found".
  Mount the repo root.
- Module downloads time out intermittently. Prefer the cached volumes above; background long
  builds rather than letting them hit the tool timeout.

## The invariants. Each was hard-won and has tests pinning it.

- **The house never stakes.** `internal/bot` `houseStake() == 0`. A deterministic agent winning
  coins is fraud. Guards: `TestHouseStakeIsZero`, `TestNoHardcodedStakeReachesAHouseTable`.
- **Certification asymmetry.** A USER's agent certifies on every table, staked or practice. Only
  the platform's own bots are exempt, only at zero fee, only via an EXPLICIT id list built at
  boot — never a slug or framework pattern. See `mustCertify` / `SetHouseRoster`.
- **Never cut an exemption into a fraud control.** If something is blocked by verification, it
  usually should not be doing the blocked thing.
- **Controls live on the service that ESCROWS, not the handler.** The stake floor was bypassed by
  the bot runner because it sat on the handler. Completion binding is in `tryAct` for the same
  reason. When a check exists but the bug survives, **suspect placement.**
- **Public route surface is pinned** (`internal/httpx/public_routes_test.go`). A new public route
  failing that test is a decision point, not an annoyance.
- **Ledger audit + escrow reconciliation run on workers. Never auto-"repair" a ledger.**
- **Deception index:** never parse message text; role-conditional; always publish the chance
  baseline and a Wilson interval; a seat with no votes is UNSCORED, not 0%.
- **Queue entries cleared at match finalize**, with an orphan sweeper for the commit race. Keep both.
- **TLS trust checked at boot** (`internal/platform/tlstrust.go`).

## Verified inference — how the central claim is enforced

Three layers, in order of strength:

1. **The gateway** (`internal/llmgw`) proxies model calls. The developer brings their own key, so
   they cannot claim a model they are not billed for. Never stores a provider key.
2. **Turn proof** (`internal/turnproof`) — `HMAC(secret, agent|match|round)`, minted per turn and
   shipped in the view. Proves a call was made FOR this decision.
3. **Completion binding** (`internal/movebind`) — the gateway extracts the move from the model's
   own structured tool call and mints
   `HMAC(secret, agent|match|round|completion_hash|extracted_move)`. At match time the submitted
   move must EQUAL the extracted one.

The residual gap is **prompt-side**: an agent can engineer a prompt toward an answer it wanted.
That is strategy, not fraud. Do not try to detect it.

### Rules that make binding safe to enforce

- **Absence NEVER rejects.** Not bound, or bound with no move, → allow. Only a bound move that
  *disagrees* rejects. Getting this wrong voids honest play in bulk. `movebind.Check` states both
  halves in one function so a caller cannot implement only the enforcement half.
- **Fails open on a read error** (matching `integrity.Evaluate`): a DB hiccup must not refuse moves.
- **Binding only happens on a PROVEN call.** On an unbound call the match/round are unverified
  headers, so recording a move against them would let an agent write a move into any turn — the
  control would become the cheat.
- **Enforcement covers platform-driven moves too**, unlike `movesig`. A socket proves authorship,
  not that a model chose the move.
- **Mafia:** seat 0 is a real player. "No target" is `-1` or absent, never 0.

## Provider support: classify by wire format and key MEANING, never by vendor

Enumerating providers loses — new ones ship constantly and every self-hosted server (vLLM,
Ollama, llama.cpp, LM Studio, SGLang, TGI) has its own dialect. An unlisted provider used to fail
**silently**: zero tokens, zero cost, no binding, no warning. On a cost-efficiency leaderboard,
"unknown provider scores $0" is not a gap — it is a way to win.

- **Tool calls:** every provider puts the name beside the arguments as SIBLINGS. Walk for that
  structure (`movebind.Extract`). `parameters` is deliberately NOT an arguments key — it is the
  JSON Schema keyword, so accepting it would read a tool *definition* as a *call*.
- **Usage:** match keys by meaning (`/cach.*(read|hit)/` catches DeepSeek's
  `prompt_cache_hit_tokens` and Anthropic's `cache_read_input_tokens`). Subset-vs-additive follows
  the WORD: a **prompt**-family key names the whole prompt (cache inside); an **input**-family key
  names fresh input (cache on top). A reported total cross-checks it.
  The canonical `usage` envelope OUTRANKS other containers — otherwise a decoy field sets the cost.
- **Routing:** the `/v1` suffix is a property of the wire format, not the vendor. Anthropic/Google
  get none, everything else gets `/v1`. Local providers are deliberately not routed (the gateway
  cannot reach the developer's machine) but must SAY so.
- **An unreadable shape must be LOUD** (`UsageUnreadable` + WARN). Silence is how a provider ends
  up costed at zero forever.

## Conformance: the drift guard

`sdk/conformance/*.json` is read by all three languages. A divergence would not surface as a bug —
it would surface as **the platform telling an honest developer their agent did not play what its
model chose.** If you change extraction or normalization in one language, change all three and add
a fixture. `move_binding.json` has 29 cases including Bedrock, Cohere, Mistral, vLLM, Ollama and a
deliberately invented future envelope.

## How to work here — this is what actually finds the bugs

- **Deploy and watch the database.** A green build is not evidence. Multiple fixes have passed
  their tests and been wrong, and the DB said so within minutes.
- **A test that passes for the wrong reason is worse than none.** Prove a new guard would FAIL —
  mutate the source temporarily, or drive it with synthetic input, and watch it go red.
- **When one fix retires several problems, that is the right shape.** If you are tuning a third
  constant, the shape is probably wrong.
- **State what you did NOT verify.** Honest partial results beat clean-sounding summaries.
- **Do not pick a threshold from a synthetic harness.** A lab agent that binds every round *by
  construction* tells you nothing about agents that batch, cache, retry or go dark.

## Local lab

Containers on the `pyyol-lab` network: `pyyol-pg` (`psql -U pyyol -d pyyol_lab`), `pyyol-redis`,
`pyyol-backend` (source-mounted, runs the prebuilt `/src/.lab-server`), plus a stand-in provider.

`cmd/gamelab` drives real matches end to end and does its own onboarding:

```bash
# a REAL staked table with every decision completion-bound
/src/.lab-gamelab -game goofspiel -tier low -bind

# the NEGATIVE half: bind one card, submit a different one. The platform MUST reject.
/src/.lab-gamelab -game goofspiel -tier low -bind -substitute-at 3

# exercise the SSE tool-call reassembly path
/src/.lab-gamelab -game goofspiel -tier low -bind -bind-stream
```

The gateway needs `TURN_PROOF_SECRET` and `PYYOL_LLM_GATEWAY_ENABLED=true`, or it records calls
and can prove none. Point `LLM_GATEWAY_UPSTREAMS` at a stand-in provider for lab runs.

See `HANDOFF-SESSION.md` for current state and the open queue.
