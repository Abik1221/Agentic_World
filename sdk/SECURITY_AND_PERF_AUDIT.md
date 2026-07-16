# Pyyol SDK — Security & Performance Audit (2026-07-16)

Deep audit of both official SDKs (`sdk/python/pyyol`, `sdk/js/src`) — the CLI + library.
Two independent read-only Opus passes (application-security; performance / robustness /
low-bandwidth). Every finding below was re-verified against the code; fixes were
applied and re-verified live (both SDKs connect over WSS and play a sandbox match with
`fallbacks:0 socket:true`).

**Bottom line:** no critical vulnerabilities. The transport/signing/replay core, the
money-safety model, and the credential store were already sound. This pass hardened
the edges (cleartext-transport warnings, Windows opener, file perms, path traversal)
and made the CLI meaningfully faster + resilient on bad networks. Verification: Python
ruff clean + 44 pytest; JS build clean + 28 node:test; live dev drive both languages.

Legend: **[FIXED]** changed + verified · **[OPEN]** documented, deferred (rationale given).

---

## Security

### [FIXED] F1 — JS Windows browser-opener used `shell:true` (arg/command injection)
`js/src/login.ts`. The auth URL always contains `&` and `--dashboard` is caller-supplied,
so `spawn(cmd, [url], {shell:true})` on win32 let `cmd.exe` treat `&calc.exe&` as a
command separator. **Fix:** `shell:false` always; on Windows `spawn("cmd", ["/c","start","",url])`
with the URL as a discrete argument.

### [FIXED] F2 — Bearer token sent over cleartext http:// with no warning (both)
`cli.py` / `cli.ts` HTTP helpers. **Fix:** `_warn_insecure_transport` / `warnInsecureTransport`
prints a one-time stderr warning when credentials would go over non-loopback
`http://`; loopback (localhost/127.0.0.1/::1) is exempt for local dev.

### [FIXED] F3 — JS runtime never warned on cleartext ws:// (Python did)
`js/src/runtime.ts`. **Fix:** ported Python's `_security_check` — a `securityCheck()`
that warns (to stderr, not the upgrade-nudge channel) when connecting `ws://` to a
non-local host (the register token is sent in the clear).

### [FIXED] F4 — Credential file created with default umask, then chmod'd (perms window, both)
`credentials.py` / `credentials.ts`. **Fix:** create the file with mode `0600` from the
start (`os.open(..., 0o600)` / `writeFileSync(..., {mode: 0o600})`), so the token is
never briefly world/group-readable.

### [FIXED] F7 — Non-constant-time CSRF `state` compare in login callback (both)
`login.py` / `login.ts`. **Fix:** `hmac.compare_digest` / length-guarded
`crypto.timingSafeEqual`.

### [FIXED] F9 — `pyyol.toml` `entry` allowed path traversal / import outside the project (both)
`cli.py` `_load_agent_from_config` / `cli.ts` `loadAgentFromConfig`. **Fix:** resolve
`entry` against the project root (dir of the discovered `pyyol.toml`) and reject
absolute paths / `..` escapes before importing.

### [FIXED] F10 — Agent id not URL-encoded in the publish path (both)
`cli.py` / `cli.ts` `cmd_publish`. **Fix:** `quote`/`encodeURIComponent` the agent id +
manifest id in `/v1/agents/{id}/manifest…` (matching the other encoded path segments).

### [FIXED] F5 — Secrets accepted on argv (visible via `ps`/`/proc`) (both)
**Fix:** a one-time `_warn_argv_secret` / `warnArgvSecret` when `--token`/`--secret` are
passed on the command line, pointing to `pyyol login` (browser) or `PYYOL_TOKEN`. (The
normal browser-login path already avoids argv; `--token` remains the documented CI path.)

### [OPEN] F6 — CSRF `state` leaks via the browser-opener argv (local token injection)
`login.py`/`login.ts`. The full auth URL (with `state`) is passed to `xdg-open`/`open`,
readable from `ps` by another local user, who could then hit the loopback callback with
their own token. **Mitigations already present:** the callback binds 127.0.0.1 only and
accepts exactly one matching-state request then shuts down. **Recommended (deferred):**
a PKCE-style secret the opener never sees, coordinated with the dashboard `/cli-login`
page — needs a dashboard-side change, so out of scope for the SDK alone. Low practical
risk (requires a hostile local user in the login window).

### [OPEN] F8 — Access token delivered via query string (browser history)
The dashboard redirects `…/callback?token=…`; the token persists in browser history.
Loopback avoids proxy logging. **Recommended (deferred):** deliver the token via a URL
fragment or POST body from a tiny dashboard page — again a dashboard-side change.

### Verified correct (unchanged)
HMAC signing + replay guard (constant-time, bounded); loopback-only callback bind; WSS
token sent in the register frame body (not URL); no TLS/cert bypass; no secret logging;
JS prototype-pollution-safe config (fixed key allow-list); money-safety guardrails
(dev sandbox-locked, ranked needs explicit opt-in); Python one runtime dep, JS zero.

---

## Performance & low-bandwidth

### [FIXED] P1 — Python CLI paid for the whole library graph on every command (HIGH)
`__init__.py` eagerly imported `server` (→`http.server`), `simulator`, `signing`; `cli.py`
imported `urllib.request` (→`ssl`/`http.client`/`email`) at module top. **Fix:** made
`__init__.py` fully lazy (PEP 562 `__getattr__`) and deferred `urllib`/`signing` into the
functions that do HTTP. `import pyyol` no longer loads `http.server`/`websockets`/`urllib`.
**Result: `--version`/`--help`/`init` ≈ 120ms → ≈ 84ms.**

### [FIXED] P2 — JS fetch calls had no timeout → commands hang on a stalled network (HIGH)
`cli.ts`. **Fix:** every fetch uses `AbortSignal.timeout(10s)`; timeouts surface as
"network timed out (slow or unreachable)" instead of hanging forever.

### [FIXED] P3 — JS `process.exit()` could truncate piped stdout (breaks `--json | jq`) (HIGH)
`cli.ts`. **Fix:** set `process.exitCode` and let the event loop drain, instead of
`process.exit(code)`.

### [FIXED] P6 — WSS half-open detection relied on library defaults
`runtime.py`. **Fix:** pass explicit `ping_interval=15`/`ping_timeout=15` to the real
`websockets` connect, so a silently-dropped link is detected → reconnect. (Backoff was
already well-tuned: 1s→×2→30s, reset on clean session.)

### [FIXED] P7 — JS frame dispatch ran concurrently (out-of-order responses under a burst)
`runtime.ts`. **Fix:** chain dispatch through a promise queue so frames are handled
strictly FIFO, one at a time (matching Python's single-thread read loop); the close
handler drains the queue before settling so a final turn response isn't cut off.

### [FIXED] P11 — `@types/node` (^20) lagged the `node>=22` engine
`js/package.json` → `^22`.

### Deferred (medium/low; documented, not yet changed)
- **P4 (MED)** — Python HTTP is one-shot per call (no keep-alive/TLS reuse); `publish`
  (3 RTT) and `validate` (6 RTT) pay a fresh handshake each. Recommend reusing one
  `http.client.HTTPSConnection` per command. Biggest win on high-latency links.
- **P5 (MED)** — No client-side per-turn deadline; a slow `step()` can send a response
  the server already discarded (deterministic fallback). Recommend bounding `decide_turn`
  by `deadline_ms`/manifest timeout + a one-time "handler too slow" warning.
- **P8 (MED)** — The match-start "kicker" fires on a 1.5s timer, not the actual
  `connected`/`registered` signal (a retry loop compensates). Recommend triggering it
  from the connector's connected event.
- **P9 (LOW)** — `watch` (SSE) can't resume (drops the `id:` cursor) and drops on 30s idle.
- **P10 (LOW)** — add a global `--json` to the read commands (arenas/leaderboard/profile/
  whoami/doctor) for scripting; guard fixed-width table padding against long/unicode ids.

### Already good (keep)
Update check is opt-in + off the hot path (the upgrade nudge piggybacks on the gateway's
`registered` frame — zero extra network); `websockets` genuinely lazy; JS truly zero-dep +
tree-shakeable; reconnect/backoff sound; heartbeat on its own thread; handler crash →
fallback signal; Python flushes every output line + has a `--json` machine mode.

---

## Low-internet / offline — strategy + EMPIRICALLY TESTED behavior

The strategy: **offline commands never touch the network; networked commands fail fast
with a clear message (never hang, never traceback); the long-running runtime bounds
every connect attempt and reconnects with visible feedback + backoff.** Tested under
fault injection (connection-refused, a hung "dead-air" server that accepts TCP but never
replies, and a dead WSS endpoint):

| Scenario | Python | JS | Result |
|---|---|---|---|
| Offline commands (`--version`/`init`/`--help`) | ~0.1s | ~0.1s | instant, rc 0 (zero network) |
| Connection refused (one-shot HTTP) | ~0.2s | ~0.1s | fast, clean message |
| Dead-air server (one-shot HTTP) | ~15s (urlopen timeout) | ~10s (`AbortSignal.timeout`) | **bounded abort**, clean message |
| Dead-air WSS `dev` connect | ~10s/attempt (`open_timeout`) | ~10s/attempt (new connect timer) | **bounded**, then `reconnecting` + backoff |
| Happy path | connecting → connected → play | same | no regression |

Fixes this required (found only by testing, not by reading):
- **[FIXED] Python printed a raw traceback on any network error** (offline/refused/
  timeout/DNS) — the HTTP helpers caught only `HTTPError`, not `URLError`/timeout.
  Added a network `except` + `_net_err()` → clean messages ("connection refused…",
  "network timed out…", "cannot resolve host…").
- **[FIXED] JS WSS `dev`/`play` connect had no timeout** → a dead-air link hung forever
  (WHATWG `WebSocket` has no connect timeout). Added an explicit opening-handshake timer
  (`connectTimeoutMs`, default 10s) → on timeout, reconnect.
- **[FIXED] Silent terminal while connecting on a bad link** → both connectors now emit
  `connecting <host>…` and, on a dropped/failed attempt, `reconnecting … retrying in Ns`
  (with the reason), surfaced by the CLI feed. No more "is it frozen?" ambiguity.

Offline command matrix (no server needed): `--version`, `--help`, `init`, `logout`, and
the offline parts of `doctor` (login/config/agent-loads/sdk-version) all work with no
network; only the "platform reachable" check needs connectivity and degrades cleanly.

## Verification
- Python: `ruff` clean, `pytest` 44 passed; startup `--version`/`--help`/`init` ≈ 84ms;
  `import pyyol` loads neither `http.server` nor `websockets` nor `urllib.request`.
- JS: `tsc` build clean, `node --test` 28 passed.
- Live (both SDKs, full stack): `login → arenas → dev` connects over WSS, plays a sandbox
  match to completion (`fallbacks:0 socket:true`); loopback triggers no insecure warning;
  `--token` triggers the argv-secret note; ranked still needs explicit `--ranked`.
