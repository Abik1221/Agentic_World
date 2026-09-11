#!/usr/bin/env bash
# Install (or refresh) the trace.pyyol.com nginx vhost → 127.0.0.1:3100.
#
# Mega Hub, pyyol.com, and admin.pyyol.com already share the origin nginx on
# :80/:443 (confirmed: Host admin.pyyol.com and pyyol.com win; Host
# trace.pyyol.com fell through to Mega Hub). This script adds an exact-name
# vhost for trace.pyyol.com. It NEVER sets default_server and NEVER edits
# Mega Hub's (or anyone else's) site files.
#
# :80 being docker-proxy is not a reason to skip: that proxy is the same
# shared edge that already has the pyyol/admin vhosts. Skipping left
# Cloudflare (orange-cloud, origin :443) serving Mega Hub forever, and the
# :3110 fallback is firewalled on this VPS (Hostinger allows 22/80/443).

set -euo pipefail

ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
SRC="$ROOT/deploy/nginx/trace.pyyol.com.conf"
LE_CERT="/etc/letsencrypt/live/trace.pyyol.com/fullchain.pem"
LE_KEY="/etc/letsencrypt/live/trace.pyyol.com/privkey.pem"
SELF_DIR="/etc/nginx/ssl"
SELF_CERT="$SELF_DIR/trace.pyyol.com.crt"
SELF_KEY="$SELF_DIR/trace.pyyol.com.key"

looks_like_eye() {
  grep -qiE 'Pyyol Eye|Pyyol Lens' "$1"
}

looks_like_mega() {
  grep -qi 'Mega Hub' "$1"
}

who_owns_port() {
  local port="$1"
  if command -v ss >/dev/null 2>&1; then
    ss -tlnp 2>/dev/null | grep -E ":${port}\\s" || true
  elif command -v netstat >/dev/null 2>&1; then
    netstat -tlnp 2>/dev/null | grep -E ":${port}\\s" || true
  fi
}

pick_cert_files() {
  if [[ -f "$LE_CERT" && -f "$LE_KEY" ]]; then
    echo "$LE_CERT" "$LE_KEY"
    return
  fi
  local pem
  for pem in /etc/letsencrypt/live/*/fullchain.pem; do
    [[ -f "$pem" ]] || continue
    if openssl x509 -in "$pem" -noout -text 2>/dev/null \
         | grep -qE 'DNS:(trace\.pyyol\.com|\*\.pyyol\.com)\b'; then
      echo "$pem" "${pem%fullchain.pem}privkey.pem"
      return
    fi
  done
  mkdir -p "$SELF_DIR"
  if [[ ! -f "$SELF_CERT" || ! -f "$SELF_KEY" ]]; then
    echo "No Let's Encrypt cert for trace.pyyol.com yet — issuing a self-signed origin cert." >&2
    openssl req -x509 -nodes -newkey rsa:2048 -days 825 \
      -keyout "$SELF_KEY" -out "$SELF_CERT" \
      -subj "/CN=trace.pyyol.com" \
      -addext "subjectAltName=DNS:trace.pyyol.com" >/dev/null 2>&1 \
    || openssl req -x509 -nodes -newkey rsa:2048 -days 825 \
      -keyout "$SELF_KEY" -out "$SELF_CERT" \
      -subj "/CN=trace.pyyol.com" >/dev/null 2>&1
    chmod 600 "$SELF_KEY"
  fi
  echo "$SELF_CERT" "$SELF_KEY"
}

render_vhost() {
  local cert="$1" key="$2" dest="$3"
  local upstream="${4:-127.0.0.1}"
  sed -e "s|/etc/letsencrypt/live/trace.pyyol.com/fullchain.pem|$cert|" \
      -e "s|/etc/letsencrypt/live/trace.pyyol.com/privkey.pem|$key|" \
      -e "s|http://127.0.0.1:3100|http://${upstream}:3100|" \
      "$SRC" > "$dest"
  if grep -q default_server "$dest"; then
    echo "❌ refusing to install a default_server vhost (would steal Mega Hub / other hosts)"
    rm -f "$dest"
    return 1
  fi
}

reload_host_nginx() {
  nginx -t
  if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet nginx; then
    systemctl reload nginx
  else
    nginx -s reload
  fi
}

nginx_dump() {
  local id="$1"
  docker exec "$id" nginx -T 2>/dev/null \
    || docker exec "$id" /usr/sbin/nginx -T 2>/dev/null \
    || docker exec "$id" openresty -T 2>/dev/null \
    || true
}

nginx_test_reload() {
  local id="$1"
  docker exec "$id" nginx -t 2>/dev/null \
    || docker exec "$id" /usr/sbin/nginx -t 2>/dev/null \
    || docker exec "$id" openresty -t 2>/dev/null \
    || return 1
  docker exec "$id" nginx -s reload 2>/dev/null \
    || docker exec "$id" /usr/sbin/nginx -s reload 2>/dev/null \
    || docker exec "$id" openresty -s reload 2>/dev/null \
    || return 1
}

# True only when the container is bound to the host's 80 or 443 (what Cloudflare
# hits). "80/tcp" in inspect JSON also matches admin-web exposing 80→8095 and
# aborted the 2026-09-11 deploy before we reached etcontest-nginx (the real edge).
host_binds_edge_ports() {
  local id="$1"
  docker inspect -f '{{.HostConfig.NetworkMode}} {{range $p, $b := .NetworkSettings.Ports}}{{range $b}} {{.HostPort}}{{end}}{{end}}' "$id" 2>/dev/null \
    | grep -qE '(^| )host( |$)|(^| )80( |$)|(^| )443( |$)'
}

copy_cert_into() {
  local src="$1" id="$2" dest="$3"
  local real="$src"
  if [[ -L "$src" ]]; then
    real="$(readlink -f "$src" 2>/dev/null || python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$src")"
  fi
  docker cp "$real" "$id:$dest"
}

# Shared-edge docker nginx: already routes pyyol.com / admin.pyyol.com, so adding
# one more named vhost is the same pattern as those hosts. We never replace
# another project's file.
#
# Prior deploy (34603382234) never printed "Found shared-edge": `docker exec sh -c
# 'command -v nginx && nginx -T'` missed images with no sh, nginx only at
# /usr/sbin, or server_name on the line after the directive. docker-proxy owns
# :80/:443 — those publishers ARE the origin Cloudflare talks to.
install_into_docker_edge() {
  local cert="$1" key="$2"
  command -v docker >/dev/null 2>&1 || return 1
  echo "Docker edge candidates:"
  docker ps --format '  {{.Names}}  ports={{.Ports}}' 2>/dev/null || true
  local id name cfg dest_in injected=0 upstream netmode
  while read -r id name; do
    [[ -n "$id" ]] || continue
    cfg="$(nginx_dump "$id")"
    if ! host_binds_edge_ports "$id"; then
      continue
    fi
    echo "Origin :80/:443 is container $name ($id)"
    if [[ -z "$cfg" ]]; then
      echo "⚠️  $name binds host :80/:443 but nginx/openresty -T produced nothing (caddy/traefik?)"
      continue
    fi
    echo "Found shared-edge nginx in container $name ($id)"

    # Host file already visible (bind-mount of /etc/nginx) — just reload.
    if docker exec "$id" test -f /etc/nginx/sites-enabled/trace.pyyol.com.conf \
      || docker exec "$id" test -f /etc/nginx/conf.d/trace.pyyol.com.conf \
      || docker exec "$id" test -f /etc/nginx/sites-available/trace.pyyol.com.conf; then
      if nginx_test_reload "$id"; then
        echo "✅ reloaded $name (vhost already on the host mount)"
        injected=1
      fi
      continue
    fi

    dest_in=""
    if docker exec "$id" test -d /etc/nginx/sites-enabled 2>/dev/null; then
      dest_in="/etc/nginx/sites-enabled/trace.pyyol.com.conf"
    elif docker exec "$id" test -d /etc/nginx/conf.d 2>/dev/null; then
      dest_in="/etc/nginx/conf.d/trace.pyyol.com.conf"
    else
      echo "⚠️  $name has nginx but no sites-enabled/conf.d — skip"
      continue
    fi

    upstream="127.0.0.1"
    netmode="$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$id" 2>/dev/null || true)"
    if [[ "$netmode" != "host" ]] && ! echo "$cfg" | grep -qE 'proxy_pass https?://127\.0\.0\.1:'; then
      upstream="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}' "$id" 2>/dev/null | awk '{print $1}')"
      upstream="${upstream:-172.17.0.1}"
      echo "    $name is not host-network; proxying Eye via ${upstream}:3100"
    fi

    docker exec "$id" mkdir -p /etc/nginx/ssl /var/www/certbot || true
    if ! copy_cert_into "$cert" "$id" /etc/nginx/ssl/trace.pyyol.com.crt \
      || ! copy_cert_into "$key" "$id" /etc/nginx/ssl/trace.pyyol.com.key; then
      echo "⚠️  could not copy origin cert into $name (LE live/ paths are often dangling symlinks) — skip"
      continue
    fi
    local tmp_in
    tmp_in="$(mktemp)"
    if ! render_vhost /etc/nginx/ssl/trace.pyyol.com.crt /etc/nginx/ssl/trace.pyyol.com.key "$tmp_in" "$upstream"; then
      rm -f "$tmp_in"
      continue
    fi
    docker cp "$tmp_in" "$id:$dest_in"
    rm -f "$tmp_in"

    if nginx_test_reload "$id"; then
      echo "✅ reloaded $name with trace.pyyol.com → ${upstream}:3100"
      injected=1
    else
      echo "⚠️  nginx -t failed in $name (http2 on?) — retrying without http2"
      tmp_in="$(mktemp)"
      if ! render_vhost /etc/nginx/ssl/trace.pyyol.com.crt /etc/nginx/ssl/trace.pyyol.com.key "$tmp_in" "$upstream"; then
        rm -f "$tmp_in"
        continue
      fi
      sed -i 's/http2 on;//g' "$tmp_in" 2>/dev/null || sed -i '' 's/http2 on;//g' "$tmp_in"
      docker cp "$tmp_in" "$id:$dest_in"
      rm -f "$tmp_in"
      if nginx_test_reload "$id"; then
        echo "✅ reloaded $name with trace.pyyol.com → ${upstream}:3100"
        injected=1
      else
        echo "❌ nginx -t still failing in $name — removing the vhost we just added"
        docker exec "$id" rm -f "$dest_in" || true
      fi
    fi
  done < <(docker ps --format '{{.ID}} {{.Names}}' 2>/dev/null || true)
  [[ "$injected" -eq 1 ]]
}

verify_host_header() {
  local body http https mega_http mega_https eye_https code_http
  body="$(mktemp)"
  code_http="$(curl -sS -o "$body" -w '%{http_code}' -m 8 -H "Host: trace.pyyol.com" http://127.0.0.1/ || true)"
  mega_http=0
  looks_like_mega "$body" && mega_http=1
  echo "Host: trace.pyyol.com on :80 → HTTP $code_http (mega=$mega_http)"

  https="$(mktemp)"
  curl -skS -o "$https" -m 8 --resolve trace.pyyol.com:443:127.0.0.1 \
    https://trace.pyyol.com/ || true
  mega_https=0
  eye_https=0
  looks_like_mega "$https" && mega_https=1
  looks_like_eye "$https" && eye_https=1
  echo "SNI trace.pyyol.com on :443 → mega=$mega_https eye=$eye_https"
  head -c 180 "$https" || true
  echo

  rm -f "$body" "$https"

  # Cloudflare Full talks to origin :443. Mega Hub on :443 is the live bug.
  if [[ "$eye_https" -eq 1 && "$mega_https" -eq 0 ]]; then
    return 0
  fi
  # HTTP vhost installed (redirect) is not enough on its own — CF uses :443.
  if [[ "$mega_https" -eq 1 || "$mega_http" -eq 1 ]]; then
    return 1
  fi
  return 1
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
echo "    :80 owner:  $(who_owns_port 80 | tr '\n' ' ')"
echo "    :443 owner: $(who_owns_port 443 | tr '\n' ' ')"

if [[ ! -f "$SRC" ]]; then
  echo "❌ missing nginx source: $SRC"
  exit 1
fi

mkdir -p /var/www/certbot
read -r CERT KEY < <(pick_cert_files)
echo "Using origin cert $CERT"

HOST_INSTALLED=0
if command -v nginx >/dev/null 2>&1; then
  tmp="$(mktemp)"
  if ! render_vhost "$CERT" "$KEY" "$tmp"; then
    rm -f "$tmp"
    echo "⚠️  host vhost render refused — will try docker shared-edge"
  else
    if [[ -d /etc/nginx/sites-available ]]; then
      dest="/etc/nginx/sites-available/trace.pyyol.com.conf"
      cp "$tmp" "$dest"
      mkdir -p /etc/nginx/sites-enabled
      ln -sfn "$dest" /etc/nginx/sites-enabled/trace.pyyol.com.conf
    else
      mkdir -p /etc/nginx/conf.d
      dest="/etc/nginx/conf.d/trace.pyyol.com.conf"
      cp "$tmp" "$dest"
    fi
    rm -f "$tmp"
    if reload_host_nginx; then
      echo "✅ host nginx vhost trace.pyyol.com → 127.0.0.1:3100 reloaded (not default_server)"
      HOST_INSTALLED=1
    else
      echo "⚠️  host nginx reload failed — removing our file, will try docker shared-edge"
      rm -f "$dest"
      if [[ -L /etc/nginx/sites-enabled/trace.pyyol.com.conf ]]; then
        rm -f /etc/nginx/sites-enabled/trace.pyyol.com.conf
      fi
    fi
  fi
else
  echo "⚠️  no host nginx binary"
fi

DOCKER_INSTALLED=0
if install_into_docker_edge "$CERT" "$KEY"; then
  DOCKER_INSTALLED=1
fi

if command -v certbot >/dev/null 2>&1 && [[ ! -f "$LE_CERT" ]]; then
  echo "Attempting Let's Encrypt for trace.pyyol.com (webroot)…"
  certbot certonly --webroot -w /var/www/certbot -d trace.pyyol.com \
    --non-interactive --agree-tos --keep-until-expiring \
    --register-unsafely-without-email || true
  if [[ -f "$LE_CERT" && -f "$LE_KEY" ]]; then
    echo "Let's Encrypt issued — switching the vhost onto the public cert."
    CERT="$LE_CERT"
    KEY="$LE_KEY"
    if [[ "$HOST_INSTALLED" -eq 1 ]]; then
      tmp="$(mktemp)"
      render_vhost "$CERT" "$KEY" "$tmp"
      if [[ -d /etc/nginx/sites-available ]]; then
        cp "$tmp" /etc/nginx/sites-available/trace.pyyol.com.conf
      else
        cp "$tmp" /etc/nginx/conf.d/trace.pyyol.com.conf
      fi
      rm -f "$tmp"
      reload_host_nginx || true
    fi
    install_into_docker_edge "$CERT" "$KEY" || true
  else
    echo "⚠️  certbot did not issue a cert. Self-signed origin cert remains (Cloudflare Full Strict may 526 until a public cert exists)."
  fi
fi

if verify_host_header; then
  echo "✅ Host/SNI trace.pyyol.com now serves Pyyol Eye (Mega Hub stays default_server)"
  exit 0
fi

echo "❌ Host: trace.pyyol.com still does not serve Pyyol Eye."
echo "    Named vhost did not win on the origin that answers :80/:443."
echo "    Mega Hub is untouched. Remaining human steps:"
echo "    1. Cloudflare → pyyol.com → Rules → Origin Rules → hostname trace.pyyol.com → Destination port 3110"
echo "       (only after 3110 is reachable — otherwise Cloudflare returns 522)."
echo "    2. Hostinger hPanel → VPS → Firewall → allow TCP 3110 from Any"
echo "       (ufw/iptables on the VM are not enough if the panel firewall still drops 3110)."
exit 1
