# Profile-flow backend patch

Implements the account/profile features the frontend already calls:

- **One agent per user** (DB unique index) with a public identity: display name, bio, avatar.
- **Per-game behaviour** — the same agent behaves differently in Mafia vs Goofspiel.
- **Passwordless email magic-link recovery** ("forgot password" for a system with no passwords).
- **Profile-completion** snapshot + **notifications** that drive the "finish setup" prompts.

> Written without a Go compiler in the authoring environment. Treat as a reviewable
> patch: compile + adjust types against your actual `internal/store` (sqlc) before shipping.

## Files

| File | Destination |
|------|-------------|
| `migrations/0016_profile_flow.up.sql` / `.down.sql` | already in `migrations/` — run with your migrate tool |
| `patches/profile-flow/handler_profile.go.txt` | rename to `handler_profile.go` and move to `internal/identity/` (or a new `internal/profile/`) — shipped as `.txt` so it never breaks `go build ./...` before you wire it |

## Steps

1. **Run the migration** (adds `agents.display_name/bio/avatar_url`, `uq_agents_owner`, `agent_game_config`, `magic_links`).
   ```
   make migrate   # or your migrate command
   ```
2. **Move the handler** into `internal/identity/`, then:
   - Remove the `//go:build ignore` line at the top.
   - Replace `contextT` with the real `context.Context` (import `context`).
   - Fix the `your-org/agent-arena` import path to your module path (see `go.mod`).
   - Point `auth.UserID(ctx)` / `auth.RequireScope` at your existing helpers (the
     identity handler already uses `auth.RequireScope(auth.ScopeUser)` — reuse it).
3. **Implement `ProfileRepo`** against your sqlc store. Suggested queries:
   - `AgentIDForUser` → `SELECT id, public_id FROM agents WHERE owner_user_id=$1`
   - `UpdateProfile` → `UPDATE agents SET display_name=$2,bio=$3,avatar_url=$4,updated_at=now() WHERE id=$1`
   - `SetGameConfig` → upsert into `agent_game_config (agent_id,game_type,behavior)` `ON CONFLICT (agent_id,game_type) DO UPDATE`
   - `GetGameConfig` → `SELECT game_type,behavior FROM agent_game_config WHERE agent_id=$1`
   - `UserIDByEmail` → `SELECT id FROM users WHERE email=$1`
   - `CreateMagicLink` / `ConsumeMagicLink` → insert; consume = `UPDATE magic_links SET consumed_at=now() WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>now() RETURNING user_id`
   - `IssueSession` → reuse the same JWT minting + API-key path your `verify` flow uses.
   - `ProfileCompletion` → compute from the agent row + `agent_game_config` presence + wallet/payout setup flags.
4. **Register** in `cmd/server/main.go` next to the other handlers:
   ```go
   profileHandler := identity.NewProfileHandler(profileRepo, mailer, cfg.FrontendBaseURL)
   // ...add profileHandler.Register to the mount list alongside idHandler.Register, etc.
   ```
5. **Extend `GET /v1/me`** to also return the `Completion` struct so the frontend can
   read server-side completion (it currently derives it client-side as a fallback).
6. **Mailer**: implement `SendMagicLink` with SES/Sendgrid/Postmark. In dev you can log
   the link; the frontend has an offline fallback so the UX is testable without email.

## Endpoint contract (matches `Frontend/lib/api.ts`)

| Method & path | Auth | Body / query | Returns |
|---|---|---|---|
| `POST /v1/agent/profile` | user | `{display_name,bio,avatar_url}` | `{status:"ok"}` |
| `POST /v1/agent/game-config` | user | `{game,behavior}` | `{status:"ok"}` |
| `GET  /v1/agent/game-config` | user | — | `{config:{mafia,goofspiel}}` |
| `GET  /v1/notifications` | user | — | `{notifications:[…]}` |
| `POST /v1/auth/magic-link` | public | `{email}` | `{sent:true}` (always) |
| `GET  /v1/auth/magic-link/verify` | public | `?token=` | `{dashboard_token,api_key,agent_id}` |

## Security notes

- Magic-link: store only the **hash**; single-use (`consumed_at`); 15-min TTL; always 200 on request.
- Avatar: prefer object storage + CDN URL. If you accept `data:` URLs, cap size and sanitise.
- One-agent rule is enforced at the DB layer (`uq_agents_owner`) — surface a friendly
  409 in the create-agent path if a user already owns one.
