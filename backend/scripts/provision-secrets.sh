#!/usr/bin/env bash
#
# provision-secrets.sh — generate and set the GitHub Actions secrets for a mainnet
# cutover, without any secret material ever leaving this machine.
#
# WHY IT WORKS THIS WAY
#
# Every value below is generated locally by openssl/solana-keygen and piped straight into
# `gh secret set` over stdin. Nothing is echoed, nothing is written to a file, nothing is
# pasted into a chat window or a terminal history. That is the whole design: a secret that
# has been displayed is a secret that has been disclosed, and the hot-wallet key here can
# move real user funds.
#
# The one exception is the hot-wallet keypair, which MUST be created deliberately and
# backed up before it holds anything — see the SOLANA section. A generated-and-forgotten
# payout key means the float is unrecoverable.
#
# USAGE
#   gh auth login                       # interactive, scoped, revocable — no PAT on disk
#   ./provision-secrets.sh check        # what is set vs missing, per repo (default)
#   ./provision-secrets.sh generate     # create + set the derivable secrets
#   ./provision-secrets.sh solana       # guided hot-wallet + cold-wallet setup
#
# Requires: gh (authenticated), openssl. `solana` + `solana-keygen` for the solana step.
set -euo pipefail

ARENA_REPO="${ARENA_REPO:?set ARENA_REPO, e.g. export ARENA_REPO=youruser/Agentic_World}"
CLIENT_REPO="${CLIENT_REPO:?set CLIENT_REPO, e.g. export CLIENT_REPO=youruser/Pyyol_client}"
ADMIN_REPO="${ADMIN_REPO:?set ADMIN_REPO, e.g. export ADMIN_REPO=youruser/Super_Admin}"

have() { command -v "$1" >/dev/null 2>&1; }
have gh || { echo "gh CLI not found: https://cli.github.com" >&2; exit 1; }
have openssl || { echo "openssl not found" >&2; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "run 'gh auth login' first" >&2; exit 1; }

# set_secret REPO NAME <<< value   — reads the value from stdin, never from argv.
# Values passed as arguments land in the process table and the shell history; stdin does
# not. This is the only way a secret is written in this script.
set_secret() { gh secret set "$2" --repo "$1" --body "$(cat)" >/dev/null && echo "  set  $2"; }

# rand N — N bytes of CSPRNG as base64, no newline.
rand() { openssl rand -base64 "$1" | tr -d '\n'; }

# ── the secrets this script can generate safely ─────────────────────────────────────
# Anything whose value is arbitrary-but-strong: signing keys, peppers, shared secrets.
# NOT included: addresses, keypairs, third-party keys (Stripe, Dockerhub, Privy) — those
# come from elsewhere and cannot be invented here.
generate() {
  echo "== $ARENA_REPO =="
  # 64 bytes: signs every dashboard JWT. Rotating it logs everyone out, which is the
  # correct blast radius for a signing key and the reason it is not shared with anything.
  rand 64 | set_secret "$ARENA_REPO" JWT_SIGNING_KEY
  # Peppers the API-key hashes. Rotating it invalidates every issued agent key, so treat
  # it as permanent once developers hold keys.
  rand 32 | set_secret "$ARENA_REPO" API_KEY_PEPPER
  # Encrypts the agent endpoint bearer token at rest. Falls back to API_KEY_PEPPER when
  # unset, so a distinct value here means one compromise does not become two.
  rand 32 | set_secret "$ARENA_REPO" AGENT_ENDPOINT_SECRET_KEY
  # Guards GET /metrics, which otherwise publishes the route table and business counters.
  rand 32 | set_secret "$ARENA_REPO" METRICS_TOKEN
  # Mints the per-turn proof tokens. Unset ⇒ no proof exists ⇒ ranked integrity has
  # nothing to enforce on, so set it well before you intend to turn enforcement up.
  rand 32 | set_secret "$ARENA_REPO" TURN_PROOF_SECRET
  # Telemetry: ingest and query are gated by SEPARATE keys, so a leaked read key cannot
  # forge spans. Both must match the Lens side (see the mapping printed by 'check').
  local ingest query
  ingest="$(rand 32)"; query="$(rand 32)"
  printf '%s' "$ingest" | set_secret "$ARENA_REPO" PYYOL_LENS_API_KEY
  printf '%s' "$ingest" | set_secret "$ARENA_REPO" INGEST_API_KEY
  printf '%s' "$query"  | set_secret "$ARENA_REPO" PYYOL_LENS_QUERY_API_KEY
  printf '%s' "$query"  | set_secret "$ARENA_REPO" QUERY_API_KEY
  rand 32 | set_secret "$ARENA_REPO" PYYOL_LENS_SESSION_SECRET

  echo "== $ADMIN_REPO =="
  rand 64 | set_secret "$ADMIN_REPO" JWT_SECRET
  rand 32 | set_secret "$ADMIN_REPO" ADMIN_POSTGRES_PASSWORD
  # The admin reads telemetry with the QUERY key, never the ingest key: a read plane that
  # holds a write credential can forge the data it is reading.
  printf '%s' "$query" | set_secret "$ADMIN_REPO" LENS_API_KEY

  echo
  echo "Platform bus (Ed25519 pair — the two repos MUST match):"
  echo "  cd backend && go run ./cmd/platform-bus-keygen"
  echo "  → PLATFORM_ENGINE_PRIVATE_KEY + PLATFORM_ADMIN_PUBLIC_KEY  → $ARENA_REPO"
  echo "  → PLATFORM_ADMIN_PRIVATE_KEY  + PLATFORM_ENGINE_PUBLIC_KEY → $ADMIN_REPO"
  echo "  A mismatch means the arena rejects admin config and the admin rejects arena"
  echo "  events — both silently degrade to last-known-good rather than erroring."
}

# ── Solana custody: guided, never automatic ─────────────────────────────────────────
solana() {
  have solana-keygen || { echo "solana-keygen not found: https://docs.solanalabs.com/cli/install" >&2; exit 1; }
  cat <<'NOTE'
This step is deliberately NOT automated. A payout key generated by a script and never
backed up is a float you cannot recover, and a hot-wallet key that has ever been printed,
logged or pasted must be considered public.

Do this on a machine you trust, and back the seed phrase up OFFLINE before funding it.

  1. COLD wallet — the reserve. Generate on an OFFLINE machine or use a hardware wallet.
     The server must never hold this key. You need only its ADDRESS here.

  2. HOT wallet — signs payouts, holds a small float:
       solana-keygen new --no-bip39-passphrase -o hot-wallet.json
       solana-keygen pubkey hot-wallet.json          # → SOLANA_PLATFORM_OWNER
     Create its USDC token account, then fund it with SOL for fees:
       spl-token create-account EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v \
         --owner <hot-pubkey> --fee-payer hot-wallet.json    # → SOLANA_PLATFORM_ATA

  3. Encrypt the key at rest — the raw key never becomes a GitHub secret:
       cd backend && go run ./cmd/wallet-secret-encrypt
     Paste the base58 private key when prompted; it prints SECRET_ENC + ENC_KEY.
       → SOLANA_HOT_WALLET_SECRET_ENC   and   SOLANA_HOT_WALLET_ENC_KEY

  4. Then set them WITHOUT echoing (gh reads stdin; -s hides your typing):
       read -rs V && printf '%s' "$V" | gh secret set SOLANA_HOT_WALLET_SECRET_ENC --repo REPO --body "$(cat)"

  5. Shred the local keypair once the encrypted form is stored and backed up:
       shred -u hot-wallet.json     # or: rm -P on macOS

REMEMBER: the mint, RPC URL and explorer are DERIVED from SOLANA_CLUSTER. Do not set them.
NOTE
}

# ── what is set vs what the code needs ─────────────────────────────────────────────
check() {
  local arena_req=(JWT_SIGNING_KEY API_KEY_PEPPER POSTGRES_PASSWORD DOCKERHUB_USERNAME
    DOCKERHUB_TOKEN DOCKERHUB_REPO SSH_HOST SSH_USER DEPLOY_SSH_KEY BASE_URL
    CORS_ALLOWED_ORIGINS PLATFORM_ENGINE_PRIVATE_KEY PLATFORM_ADMIN_PUBLIC_KEY
    SOLANA_CLUSTER SOLANA_PLATFORM_OWNER SOLANA_PLATFORM_ATA
    SOLANA_HOT_WALLET_SECRET_ENC SOLANA_HOT_WALLET_ENC_KEY
    SOLANA_COLD_WALLET_ADDRESS HOT_WALLET_CAP_CENTS HOT_WALLET_MIN_SOL_LAMPORTS
    HCAPTCHA_SECRET METRICS_TOKEN TURN_PROOF_SECRET)
  local client_req=(NEXT_PUBLIC_API_BASE NEXT_PUBLIC_SITE_URL NEXT_PUBLIC_SOLANA_RPC_URL
    DOCKERHUB_USERNAME DOCKERHUB_TOKEN DOCKERHUB_REPO SERVER_HOST SERVER_USER DEPLOY_SSH_KEY)
  local admin_req=(JWT_SECRET ADMIN_POSTGRES_PASSWORD ADMIN_CORS_ORIGIN
    PLATFORM_ADMIN_PRIVATE_KEY PLATFORM_ENGINE_PUBLIC_KEY LENS_API_KEY
    DOCKERHUB_USERNAME DOCKERHUB_TOKEN DOCKERHUB_REPO SSH_HOST SSH_USER DEPLOY_SSH_KEY)

  local missing=0
  report() {
    local repo="$1"; shift
    echo "== $repo =="
    local have_list; have_list="$(gh secret list --repo "$repo" --json name -q '.[].name' 2>/dev/null || true)"
    if [ -z "$have_list" ]; then echo "  (cannot list — check access to $repo)"; return; fi
    for name in "$@"; do
      if printf '%s\n' "$have_list" | grep -qx "$name"; then
        echo "  ok      $name"
      else
        echo "  MISSING $name"; missing=$((missing+1))
      fi
    done
  }
  report "$ARENA_REPO"  "${arena_req[@]}"
  report "$CLIENT_REPO" "${client_req[@]}"
  report "$ADMIN_REPO"  "${admin_req[@]}"

  cat <<'NOTE'

Values that MUST be identical across repos (a mismatch fails silently, not loudly):
  arena PYYOL_LENS_API_KEY        == arena INGEST_API_KEY        (ingest / write)
  arena PYYOL_LENS_QUERY_API_KEY  == arena QUERY_API_KEY  == admin LENS_API_KEY  (read)
  arena PLATFORM_ENGINE_PRIVATE_KEY ⇄ admin PLATFORM_ENGINE_PUBLIC_KEY   (same pair)
  admin PLATFORM_ADMIN_PRIVATE_KEY  ⇄ arena PLATFORM_ADMIN_PUBLIC_KEY    (same pair)
  client NEXT_PUBLIC_SOLANA_RPC_URL network == arena SOLANA_CLUSTER network

DERIVED from SOLANA_CLUSTER — leave unset unless you are deliberately overriding:
  SOLANA_USDC_MINT, SOLANA_RPC_URL, SOLANA_EXPLORER_TX

Optional (unset = feature off, which is a valid state):
  STRIPE_* (card top-ups; unset ⇒ Solana-only), GOOGLE_CLIENT_ID, PRIVY_* (unused — the
  frontend has no Privy dependency), TRUSTED_PROXY_COUNT (defaults to 1 = one nginx hop).
NOTE
  [ "$missing" -gt 0 ] && echo "$missing secret(s) missing." && return 1
  echo "All required secrets present."
}

case "${1:-check}" in
  generate) generate ;;
  solana)   solana ;;
  check)    check ;;
  *) echo "usage: $0 [check|generate|solana]" >&2; exit 2 ;;
esac
