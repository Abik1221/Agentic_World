#!/usr/bin/env bash
# Install (or refresh) the trace.pyyol.com nginx vhost → 127.0.0.1:3100.
#
# Mega Hub shares this VPS and is the origin default on :80 (confirmed: Host
# trace.pyyol.com on :80 returns Mega Hub). This script NEVER sets default_server
# and NEVER edits Mega Hub's files.
#
# If host nginx is the process on :80/:443, a named vhost wins that Host header.
# If a docker-proxy owns :80 (Mega Hub container), we do not inject into that
# project — Eye stays on its own port (3110) and Cloudflare origin-rules send
# the hostname there. Exiting 0 in that case is deliberate so a tracing deploy
# cannot take down Mega Hub.

set -euo pipefail

ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
SRC="$ROOT/deploy/nginx/trace.pyyol.com.conf"
LE_CERT="/etc/letsencrypt/live/trace.pyyol.com/fullchain.pem"
LE_KEY="/etc/letsencrypt/live/trace.pyyol.com/privkey.pem"
SELF_DIR="/etc/nginx/ssl"
SELF_CERT="$SELF_DIR/trace.pyyol.com.crt"
SELF_KEY="$SELF_DIR/trace.pyyol.com.key"

looks_like_eye() {
  grep -qiE 'Pyyol Eye|Pyyol Lens|Sign in' "$1"
}

who_owns_port() {
  local port="$1"
  if command -v ss >/dev/null 2>&1; then
    ss -tlnp 2>/dev/null | grep -E ":${port}\\s" || true
  elif command -v netstat >/dev/null 2>&1; then
    netstat -tlnp 2>/dev/null | grep -E ":${port}\\s" || true
  fi
}

echo "Waiting for Pyyol Eye on 127.0.0.1:3100…"
ok=0
for i in $(seq 1 30); do
  if curl -fsS -m 3 http://127.0.0.1:3100/ >/dev/null 2>&1; then ok=1; break; fi
  sleep 2
done
if [[ "$ok" -ne 1 ]]; then
  echo "❌ web dashboard never answered on 127.0.0.1:3100"
  exit 1
fi
echo "✅ Pyyol Eye listening on loopback :3100"

if [[ -f "$SRC" ]] && command -v nginx >/dev/null 2>&1; then
  listeners80="$(who_owns_port 80)"
  if echo "$listeners80" | grep -qi docker-proxy; then
    echo "⚠️  :80 is docker-proxy (Mega Hub). Not writing a host vhost that cannot bind :80."
    echo "    Eye remains on :3100 (loopback) + :3110 (public). Cloudflare origin-rule must send trace.pyyol.com → :3110."
    exit 0
  fi

  mkdir -p /var/www/certbot
  cert="$LE_CERT"
  key="$LE_KEY"
  if [[ ! -f "$LE_CERT" || ! -f "$LE_KEY" ]]; then
    mkdir -p "$SELF_DIR"
    if [[ ! -f "$SELF_CERT" || ! -f "$SELF_KEY" ]]; then
      echo "No Let's Encrypt cert for trace.pyyol.com yet — issuing a self-signed origin cert."
      openssl req -x509 -nodes -newkey rsa:2048 -days 825 \
        -keyout "$SELF_KEY" -out "$SELF_CERT" \
        -subj "/CN=trace.pyyol.com" \
        -addext "subjectAltName=DNS:trace.pyyol.com" >/dev/null 2>&1 \
      || openssl req -x509 -nodes -newkey rsa:2048 -days 825 \
        -keyout "$SELF_KEY" -out "$SELF_CERT" \
        -subj "/CN=trace.pyyol.com" >/dev/null 2>&1
      chmod 600 "$SELF_KEY"
    fi
    cert="$SELF_CERT"
    key="$SELF_KEY"
  fi

  tmp="$(mktemp)"
  sed -e "s|/etc/letsencrypt/live/trace.pyyol.com/fullchain.pem|$cert|" \
      -e "s|/etc/letsencrypt/live/trace.pyyol.com/privkey.pem|$key|" \
      "$SRC" > "$tmp"

  if grep -q default_server "$tmp"; then
    echo "❌ refusing to install a default_server vhost (would steal Mega Hub / other hosts)"
    rm -f "$tmp"
    exit 1
  fi

  if [[ -d /etc/nginx/sites-available ]]; then
    dest="/etc/nginx/sites-available/trace.pyyol.com.conf"
    cp "$tmp" "$dest"
    mkdir -p /etc/nginx/sites-enabled
    ln -sfn "$dest" /etc/nginx/sites-enabled/trace.pyyol.com.conf
  else
    mkdir -p /etc/nginx/conf.d
    cp "$tmp" /etc/nginx/conf.d/trace.pyyol.com.conf
  fi
  rm -f "$tmp"

  nginx -t
  if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet nginx; then
    systemctl reload nginx
  else
    nginx -s reload || true
  fi
  echo "✅ nginx vhost trace.pyyol.com → 127.0.0.1:3100 reloaded (not default_server)"

  body="$(mktemp)"
  curl -s -o "$body" -m 8 -H "Host: trace.pyyol.com" http://127.0.0.1/ || true
  if grep -qi 'Mega Hub' "$body"; then
    echo "⚠️  Host: trace.pyyol.com on :80 still serves Mega Hub. Named vhost did not win;"
    echo "    leaving Mega Hub untouched. Cloudflare origin-rule → :3110 is the surviving path."
    rm -f "$body"
    exit 0
  fi
  if looks_like_eye "$body"; then
    echo "✅ Host: trace.pyyol.com on :80 now serves Pyyol Eye"
  else
    echo "⚠️  Host: trace.pyyol.com on :80 did not look like Eye; Cloudflare origin-rule → :3110 still applies."
  fi
  rm -f "$body"
else
  echo "⚠️  host nginx not available — Mega Hub keeps :80. Eye is on :3100/:3110."
fi
exit 0
