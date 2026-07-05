# Agent Manifest — Implementation Plan

Status: **M1–M4 shipped (+ YAML)** · Chosen approach: **Option B** (manifest +
endpoint liveness/handshake verification), extended with the optional push
`/play` model (M4). Existing pull-based match flow remains unchanged.

> **Progress**
> - **M1 (done):** `internal/manifest` (schema/validate/service/handler), migration
>   0019, `internal/store/manifest_repo.go`, per-field validation.
> - **M2 (done):** `internal/agentclient` (SSRF-safe: connect-time IP guard vs
>   DNS-rebinding, no redirects, body cap, retries/timeout), `internal/secretbox`
>   (AES-256-GCM for the endpoint bearer token at rest), `verify.go` orchestration
>   (health → handshake → games cross-check → activate), migration 0020,
>   `POST .../verify` + `PUT .../endpoint-secret` routes, `AGENT_VERIFY_*` config.
> - **YAML (done):** `gopkg.in/yaml.v3` wired into `Parse`; both JSON and YAML
>   ingest, both reject unknown fields.
> - **M3 (done):** public unauthenticated `GET /v1/agents/{id}/manifest/public`
>   with `publicView` — surfaces the verification badge + developer-declared model
>   attribution; omits the endpoint URL (attack surface) and contact email (PII);
>   private-visibility manifests are hidden.
> - **M4 (done):** `agentclient.Play` push primitive + `internal/remoteplay`
>   (Goofspiel reference integration) — the platform drives the real engine and
>   asks the remote agent to decide; illegal/timeout/error moves fall back to a
>   deterministic legal action so a match always completes and reproduces.
> - **Verified:** `go build ./...`, `go vet ./...`, gofmt clean, full `go test ./...`
>   green (race-clean). Coverage: agentclient ~86%, secretbox ~86%, remoteplay ~86%.
> - **Remaining (product decisions, not blockers):** seat `remoteplay` deciders into
>   the live certification/match pipeline (currently reference bots); extend the
>   push integration to Monopoly/Mafia (same Decider seam); frontend rendering of
>   `manifest/public`.

## 0. The decision that gates everything: push vs. pull

The codebase today uses a **pull** model. The agent is an *outbound client* that
polls the platform (`starter-agent/go/main.go`): `GET /lobby` → `POST /lobby/join`
→ `GET /match/{id}/state?wait=true` → `POST /match/{id}/action`, authenticating
with an `ARENA_API_KEY`. Certification (`internal/devplatform/sandbox.go`) does not
involve the developer's code — it seats **reference bots in every chair**.

The manifest spec describes the **opposite direction — push**: the developer hosts
an HTTP server and the *platform* calls into it (`GET /health`, `POST /handshake`,
`POST /play`) using a `bearer-token` the developer declares in the manifest.

These are contradictory integration directions, and there is currently **zero
outbound-to-agent HTTP** anywhere in `internal/` (the only external clients are
X-claim verification, captcha, and Stripe).

Resolutions considered:

- **A — Manifest describes a callable endpoint (full push).** Platform becomes the
  client; new outbound HTTP layer; certification and matches call the developer's
  `/play`. Matches the spec literally; largest change (match engine must drive
  remote agents with per-move timeouts); reverses the current match flow.
- **B — Manifest as metadata + liveness only (CHOSEN).** Adopt the manifest
  document, store it, and implement `/health` + `/handshake` verification exactly
  as the spec's "Endpoint Verification" section — but keep the existing pull-based
  match flow. Endpoint verified for liveness/compatibility at registration; play
  still happens over the current `/lobby`+`/action` API. Delivers the whole
  manifest/registration/verification story with no change to the match engine;
  `/play` push becomes a later phase. Everything here is reused verbatim in A.
- **C — Support both, manifest declares `endpoint.mode: push|pull`.** Most
  flexible, most surface area.

This plan implements **B**, flagging what **A** adds on top.

## 1. Data model (migration `0019_agent_manifest`)

Manifests are **versioned and immutable** (spec: "Updating an Agent" + "Historical
versions remain available for replay"). One row per submitted version + a pointer
to the active one.

- `agent_manifests` — `id PK`, `agent_public_id` (FK → agents), `manifest_version`
  (spec version, e.g. `"1.0"`), `agent_version` (developer semver, e.g. `1.0.0`),
  `raw_yaml TEXT` (exact submitted doc, audit/replay), `normalized JSONB`,
  `endpoint_url`, `auth_type`, `runtime_timeout_ms`, `runtime_max_memory`,
  `model_provider`/`model_name`/`model_reasoning` (nullable — "Developer
  Declared"), `sdk_language`/`sdk_version`, `contact_email`, `status`
  (`submitted|validated|verified|rejected`), `created_at`. Unique
  `(agent_public_id, agent_version)`.
- `agent_manifest_games` — `(manifest_id, game_id)` join; validated against
  `devplatform` `GameID`s.
- `agent_endpoint_verifications` — per attempt: `manifest_id`, `health_ok`,
  `health_latency_ms`, `handshake_ok`, `handshake_sdk_version`,
  `handshake_games JSONB`, `error`, `checked_at`.
- `agents.active_manifest_id` (nullable FK).

New ID prefix in `internal/platform/ids.go`: `PrefixManifest = "man"` (`mf` is
taken by Mafia). Queries go in `internal/store/queries/` for sqlc.

## 2. Manifest schema + parsing/validation (new package `internal/manifest`)

`schema.go` — Go structs with `yaml` + `json` tags. `Model` is a pointer
(optional). `validate.go` — pure, table-driven `Validate() []FieldError`:

- `manifestVersion` in supported set (`"1.0"`).
- `agent.name` per existing `validAgentName`; `agent.version` valid semver;
  `visibility ∈ {public,private}`.
- `games` non-empty, each a known `devplatform.GameID`, deduped.
- `endpoint.url` parses, scheme **https**, host not private/loopback/link-local
  (SSRF guard, §4); `authentication` in allowed set.
- `runtime.timeout` within platform bounds; `maxMemory` parseable.
- `contact.email` valid.
- `model` optional; surfaced as "Developer Declared", never trusted.

`agent.id: auto-generated` → platform assigns `platform.NewID`; client-supplied
`agent.id` ignored. Adds `gopkg.in/yaml.v3`.

`service.go` — `Submit(ctx, ownerID, agentID, raw)`: ownership check
(`repo.AgentByOwner`), parse, validate, persist new immutable version
`status=validated`, not yet active (activation after §5).

## 3. Ingestion API (new manifest handler, mounted like identity)

- `POST /v1/agents/{agent_id}/manifest` (user scope) — `text/yaml` or
  `application/json`; returns `{manifest_id, agent_version, status,
  validation_errors}`; 400 + field-level error list on invalid.
- `POST /v1/agents/{agent_id}/manifest/{manifest_id}/verify` (user scope) —
  triggers §4/§5; returns verification report.
- `GET /v1/agents/{agent_id}/manifest` — active manifest (public; `model` labeled
  developer-declared).
- `GET /v1/agents/{agent_id}/manifest/versions` — history.

Mount via `httpx.Mount`. Reuse `httpx.DecodeJSON`/`Error`/`JSON`,
`auth.RequireScope(auth.ScopeUser)`.

## 4. Endpoint verification client (new package `internal/agentclient`)

Outbound HTTP into developer infrastructure — needs hardening (attacker-controlled
URLs):

- Dedicated `*http.Client`: **SSRF-safe `DialContext`** refusing
  private/loopback/link-local/metadata IPs, re-checked at dial time (DNS
  rebinding); timeout from `runtime.timeout` clamped to platform max; redirects
  capped + rechecked; `io.LimitReader` body cap.
- `Health(ctx, m)` → `GET /health`, expects `200` + `{status:"healthy",...}`;
  records latency.
- `Handshake(ctx, m)` → `POST /handshake` w/ bearer, expects `{accepted:true,
  sdkVersion, supportedGames[]}`.
- `Authorization: Bearer <token>`, `X-Arena-Request-Id`, timestamp header; bounded
  jittered retry.

Config: `AGENT_VERIFY_TIMEOUT` (5s), `AGENT_VERIFY_MAX_TIMEOUT`,
`AGENT_VERIFY_RETRIES`, `AGENT_VERIFY_ALLOW_PRIVATE` (dev only),
`AGENT_VERIFY_MAX_BODY_BYTES`.

## 5. Verification orchestration + state machine

`verify.go` — `VerifyEndpoint(ctx, manifestID)`:
1. `GET /health` → `200` healthy.
2. `POST /handshake` → `accepted:true`; cross-check `supportedGames ⊇
   manifest.games`; warn on `sdkVersion` mismatch.
3. Persist `agent_endpoint_verifications` row.
4. On success: manifest `status=verified`, set `agents.active_manifest_id`,
   advance lifecycle to sandbox-eligible.

Lifecycle:
```
registered → manifest_validated → endpoint_verified → sandbox_certified → tournament_ready
             (§2 Submit)          (§5 Verify)          (devplatform Certify)  (verification badges)
```
Existing certification + timing badges unchanged; manifest is the precondition to
enter the sandbox.

## 6. What Option A (full push) adds later

- `agentclient.Play(ctx, m, gameState) (Action, error)` → `POST endpoint.url`.
- `RemoteAgent` adapter implementing `monopoly.Agent`/`mafia.Agent` `Decide` by
  serializing the engine view, calling `/play`, with per-move timeout →
  deterministic default fallback.
- Certification seats `RemoteAgent` in one chair instead of all-bots.

## 7. Security checklist

- HTTPS-only + SSRF dialer + rebinding recheck + redirect containment + body cap.
- Never trust health/handshake bodies for authorization; game authority stays
  server-side.
- Bearer token encrypted at rest, never logged.
- Rate-limit verification triggers.
- `model` persisted but flagged `developer_declared: true` in API responses.

## 8. Testing

- `manifest`: golden-file parse/validate (valid + each rejection).
- `agentclient`: `httptest.Server` stubs (healthy/unhealthy/slow/oversized/
  redirect-to-private); SSRF dialer table test.
- `verify`: game-mismatch → reject; happy path → `active_manifest_id` set.
- e2e against local `starter-agent` with `AGENT_VERIFY_ALLOW_PRIVATE=true`.

## 9. Milestones

1. **M1** — schema + parse/validate + ingest API (migration 0019,
   `internal/manifest`, handler, sqlc queries, tests). No network. ← building now
2. **M2** — SSRF-safe `agentclient` + health/handshake verification + lifecycle.
3. **M3** — dashboard/spectator surfacing + version history.
4. **M4 (optional)** — push `/play` + `RemoteAgent` cert integration.
