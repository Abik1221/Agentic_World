#!/usr/bin/env bash
# Install (or refresh) the trace.pyyol.com nginx vhost → 127.0.0.1:3100.
#
# Why this exists: Mega Hub is the origin default_server on this VPS. Without a
# named vhost, Host: trace.pyyol.com is stolen by Mega Hub. This script never
# marks the Eye vhost default_server and never edits Mega Hub's config.
#
# Run from the tracing tree on the host (deploy-tracing.yml does this).

set -euo pipefail

ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
SRC="$ROOT/deploy/nginx/trace.pyyol.com.conf"
LE_CERT="/etc/letsencrypt/live/trace.pyyol.com/fullchain.pem"
LE_KEY="/etc/letsencrypt/live/trace.pyyol.com/privkey.pem"
SELF_DIR="/etc/nginx/ssl"
SELF_CERT="$SELF_DIR/trace.pyyol.com.crt"
SELF_KEY="$SELF_DIR/trace.pyyol.com.key"

if [[ ! -f "$SRC" ]]; then
  echo "❌ missing nginx source: $SRC"
  exit 1
fi
if ! command -v nginx >/dev/null 2>&1; then
  echo "❌ nginx is not installed on the host; cannot bind trace.pyyol.com"
  exit 1
fi

mkdir -p /var/www/certbot

cert="$LE_CERT"
key="$LE_KEY"
if [[ ! -f "$LE_CERT" || ! -f "$LE_KEY" ]]; then
  mkdir -p "$SELF_DIR"
  if [[ ! -f "$SELF_CERT" || ! -f "$SELF_KEY" ]]; then
    echo "No Let's Encrypt cert for trace.pyyol.com yet — issuing a self-signed origin cert so SNI matches (Mega Hub stops winning 443)."
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
  # If a stale default site is also named trace.pyyol.com, leave it — exact names can coexist
  # only if they share the same upstream; we don't delete other projects' files.
else
  mkdir -p /etc/nginx/conf.d
  cp "$tmp" /etc/nginx/conf.d/trace.pyyol.com.conf
fi
rm -f "$tmp"

nginx -t
if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet nginx; then
  systemctl reload nginx
else
  nginx -s reload
fi
echo "✅ nginx vhost trace.pyyol.com → 127.0.0.1:3100 reloaded"

if command -v certbot >/dev/null 2>&1 && [[ ! -f "$LE_CERT" ]]; then
  echo "Attempting Let's Encrypt for trace.pyyol.com (webroot)…"
  certbot certonly --webroot -w /var/www/certbot -d trace.pyyol.com \
    --non-interactive --agree-tos --keep-until-expiring \
    --register-unsafely-without-email || true
  if [[ -f "$LE_CERT" && -f "$LE_KEY" ]]; then
    echo "Let's Encrypt issued — switching the vhost onto the public cert."
    exec "$0" "$ROOT"
  fi
  echo "⚠️  certbot did not issue a cert (Cloudflare Full Strict may 526 until one exists). Self-signed origin cert remains."
fi

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

body="$(mktemp)"
code="$(curl -sk -o "$body" -w '%{http_code}' -m 8 --resolve trace.pyyol.com:443:127.0.0.1 https://trace.pyyol.com/ || true)"
if grep -qi 'Mega Hub' "$body"; then
  echo "❌ Host: trace.pyyol.com still serves Mega Hub (HTTP $code). nginx server_name did not win."
  rm -f "$body"
  exit 1
fi
if ! grep -qiE 'Pyyol Eye|Pyyol Lens|Sign in' "$body"; then
  echo "⚠️  Host: trace.pyyol.com did not look like Pyyol Eye (HTTP $code). Body follows:"
  head -c 400 "$body" || true
  echo
  rm -f "$body"
  exit 1
fi
rm -f "$body"
echo "✅ trace.pyyol.com SNI/Host serves Pyyol Eye (not Mega Hub)"
