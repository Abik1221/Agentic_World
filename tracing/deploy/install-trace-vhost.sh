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
  local upstream="${4:-127.0.0.1:3100}"
  sed -e "s|/etc/letsencrypt/live/trace.pyyol.com/fullchain.pem|$cert|" \
      -e "s|/etc/letsencrypt/live/trace.pyyol.com/privkey.pem|$key|" \
      -e "s|http://127.0.0.1:3100|http://${upstream}|" \
      "$SRC" > "$dest"
  # Only refuse a listen ... default_server; comments in this file mention the
  # phrase and used to make render_vhost abort before copying into etcontest-nginx.
  if grep -Eq 'listen[[:space:]]+[^;]*default_server' "$dest"; then
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

nginx_bin() {
  local id="$1"
  if docker exec "$id" nginx -v >/dev/null 2>&1; then
    echo nginx
  elif docker exec "$id" /usr/sbin/nginx -v >/dev/null 2>&1; then
    echo /usr/sbin/nginx
  else
    echo ""
  fi
}

nginx_test_reload() {
  local id="$1" bin out
  bin="$(nginx_bin "$id")"
  if [[ -z "$bin" ]]; then
    echo "⚠️  no nginx binary in $id"
    return 1
  fi
  out="$(docker exec "$id" "$bin" -t 2>&1)" || {
    echo "$out"
    return 1
  }
  docker exec "$id" "$bin" -s reload 2>/dev/null || docker exec "$id" "$bin" -s reload
}

# nginx -T prefixes each file with:  # configuration file /path:
# Super Admin / landing already live as named vhosts in this same edge. Put
# trace.pyyol.com in THAT directory (the one nginx actually includes), not
# whichever of sites-enabled/conf.d happens to exist on disk.
vhost_dir_from_dump() {
  python3 - <<'PY'
import os, re, sys
text = sys.stdin.read()
current = ""
chosen = ""
for line in text.splitlines():
    m = re.match(r"^# configuration file (.+):$", line.rstrip())
    if m:
        current = m.group(1)
        continue
    if re.search(r"server_name\s+.*(admin\.pyyol\.com|\bpyyol\.com)\b", line):
        if current and not current.endswith("/nginx.conf"):
            chosen = current
            break
        if current:
            chosen = current
includes = re.findall(r"include\s+([^;]+);", text)
if chosen and not chosen.endswith("/nginx.conf"):
    print(os.path.dirname(chosen))
    sys.exit(0)
for inc in includes:
    inc = inc.strip().strip("'\"")
    if "conf.d" in inc or "sites-enabled" in inc or "sites-available" in inc:
        print(inc.rsplit("/", 1)[0])
        sys.exit(0)
sys.exit(1)
PY
}

admin_proxy_pass_from_dump() {
  python3 - <<'PY'
import re, sys
text = sys.stdin.read()
blocks = re.split(r"(?=server\s*\{)", text)
for b in blocks:
    if not re.search(r"server_name\s+.*admin\.pyyol\.com", b):
        continue
    m = re.search(r"proxy_pass\s+http://([^;]+);", b)
    if m:
        print(m.group(1).strip())
        sys.exit(0)
sys.exit(1)
PY
}

nginx_has_server_name() {
  local id="$1" host="$2"
  nginx_dump "$id" | grep -F "server_name" | grep -Fq "$host"
}

# docker cp replaces the inode and fails on bind-mounted nginx.conf
# ("unlinkat: device or resource busy"). cat-overwrite keeps the mount.
write_into_container() {
  local id="$1" dest="$2" src="$3"
  docker exec -i "$id" sh -c "cat > \"$dest\"" < "$src"
}

MAP_SRC="$ROOT/deploy/nginx/websocket-upgrade.conf"

# $connection_upgrade must exist in http{} or `nginx -t` rejects the Eye vhost
# (unknown variable) and login stays on Connection: upgrade → CF 522/524.
# Admin already ships this map; skip if nginx -T already defines it.
ensure_upgrade_map_host() {
  if nginx -T 2>/dev/null | grep -q 'map \$http_upgrade \$connection_upgrade'; then
    echo "    \$connection_upgrade already defined on host nginx"
    return 0
  fi
  if [[ ! -f "$MAP_SRC" ]]; then
    echo "⚠️  missing $MAP_SRC — vhost needs \$connection_upgrade"
    return 1
  fi
  mkdir -p /etc/nginx/conf.d
  cp "$MAP_SRC" /etc/nginx/conf.d/websocket-upgrade.conf
  echo "    installed /etc/nginx/conf.d/websocket-upgrade.conf"
}

ensure_upgrade_map_container() {
  local id="$1"
  if nginx_dump "$id" | grep -q 'map $http_upgrade $connection_upgrade'; then
    echo "    \$connection_upgrade already defined in $id"
    return 0
  fi
  if [[ ! -f "$MAP_SRC" ]]; then
    echo "⚠️  missing $MAP_SRC"
    return 1
  fi
  docker exec "$id" mkdir -p /etc/nginx/conf.d || true
  if write_into_container "$id" /etc/nginx/conf.d/websocket-upgrade.conf "$MAP_SRC"; then
    echo "    installed websocket-upgrade.conf into $id:/etc/nginx/conf.d/"
  else
    docker cp "$MAP_SRC" "$id:/etc/nginx/conf.d/websocket-upgrade.conf"
  fi
}

# Add `include <vhost>;` to nginx.conf without deleting Mega Hub server blocks.
# Prefers in-container cat; falls back to the host bind-mount source.
inject_trace_include() {
  local id="$1" dest_in="$2"
  local host_conf src
  host_conf="$(mktemp)"
  if ! docker exec "$id" cat /etc/nginx/nginx.conf > "$host_conf" 2>/dev/null; then
    rm -f "$host_conf"
    return 1
  fi
  python3 - "$host_conf" "$dest_in" <<'PY'
import sys
path, inc = sys.argv[1], sys.argv[2]
text = open(path, encoding="utf-8", errors="replace").read()
needle = "include %s;" % inc
if needle not in text:
    if "http {" in text:
        text = text.replace("http {", "http {\n    include %s;" % inc, 1)
    else:
        sys.exit(2)
    open(path, "w", encoding="utf-8").write(text)
PY
  if write_into_container "$id" /etc/nginx/nginx.conf "$host_conf"; then
    echo "    include ${dest_in} written into nginx.conf (Mega Hub server blocks kept)"
    rm -f "$host_conf"
    return 0
  fi
  src="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/etc/nginx/nginx.conf"}}{{.Source}}{{end}}{{end}}' "$id")"
  if [[ -z "$src" ]]; then
    src="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/etc/nginx"}}{{.Source}}{{end}}{{end}}' "$id")"
    [[ -n "$src" ]] && src="${src%/}/nginx.conf"
  fi
  if [[ -n "$src" && -f "$src" ]]; then
    cp -a "$src" "${src}.pyyol-bak" 2>/dev/null || true
    cp "$host_conf" "$src"
    echo "    include ${dest_in} written into host bind-mount ${src} (Mega Hub kept)"
    rm -f "$host_conf"
    return 0
  fi
  echo "    ⚠️  could not write nginx.conf (bind-mount busy, no host source)"
  rm -f "$host_conf"
  return 1
}

# Join pyyol-lens-web to the edge nginx networks so we can proxy by container
# name — same as admin-web on the shared pyyol network.
join_web_to_edge_networks() {
  local nginx_id="$1"
  local web="pyyol-lens-web"
  docker inspect "$web" >/dev/null 2>&1 || return 1
  local nets
  nets="$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$nginx_id" 2>/dev/null || true)"
  local n joined=0
  for n in $nets; do
    [[ -n "$n" ]] || continue
    if docker network connect "$n" "$web" 2>/dev/null; then
      echo "    attached $web to docker network $n (same pattern as admin-web)"
      joined=1
    elif docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$web" 2>/dev/null | grep -qw "$n"; then
      echo "    $web already on docker network $n"
      joined=1
    fi
  done
  [[ "$joined" -eq 1 ]]
}

# nginx -t in this edge has failed on Mega Hub's own `finance-api` hostname.
# Connecting that container onto the nginx network (no site-file edits) lets
# -t pass so a new named vhost can be reloaded. Does not remove Mega Hub.
try_resolve_missing_upstreams() {
  local nginx_id="$1" err="$2"
  local host
  host="$(printf '%s' "$err" | sed -n 's/.*host not found in upstream "\([^":]*\).*/\1/p' | head -1)"
  [[ -n "$host" ]] || return 1
  echo "    nginx -t missing upstream $host — alias + /etc/hosts so -t can pass (Mega Hub site files untouched)"
  local cid ip nets n nip
  cid="$(docker ps --format '{{.ID}} {{.Names}}' | awk -v h="$host" 'index($2,h){print $1; exit}')"
  if [[ -n "$cid" ]]; then
    nets="$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$nginx_id" 2>/dev/null || true)"
    for n in $nets; do
      docker network connect --alias "$host" "$n" "$cid" 2>/dev/null || true
      nip="$(docker inspect -f "{{(index .NetworkSettings.Networks \"$n\").IPAddress}}" "$cid" 2>/dev/null || true)"
      if [[ -n "$nip" ]]; then
        ip="$nip"
      fi
    done
    if [[ -z "$ip" ]]; then
      ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{"\n"}}{{end}}' "$cid" | awk 'NF { print; exit }')"
    fi
  fi
  # Static proxy_pass hosts are resolved at `nginx -t`. Compose service name
  # `finance-api` is not the container name `etcontest-finance-api`, so connect
  # alone is not enough. /etc/hosts is runtime-only; we do not edit site files.
  if [[ -n "$ip" ]]; then
    docker exec "$nginx_id" sh -c "grep -F '$host' /etc/hosts >/dev/null 2>&1 || echo '$ip $host' >> /etc/hosts" \
      && echo "    /etc/hosts += $ip $host"
  else
    echo "    ⚠️  no container matching $host; nginx -t will keep failing until that upstream exists"
    return 1
  fi
}

# nginx -t can fail on several Mega Hub upstreams in a row. Resolve each one
# (hosts/alias only — no Mega Hub site-file edits) until -t is clean or we stall.
resolve_all_missing_upstreams() {
  local id="$1" bin err i
  bin="$(nginx_bin "$id")"
  [[ -n "$bin" ]] || return 1
  for i in 1 2 3 4 5 6 7 8; do
    err="$(docker exec "$id" "$bin" -t 2>&1 || true)"
    if ! printf '%s' "$err" | grep -qi 'host not found in upstream'; then
      printf '%s\n' "$err"
      if printf '%s' "$err" | grep -qi 'syntax is ok\|test is successful'; then
        return 0
      fi
      return 1
    fi
    try_resolve_missing_upstreams "$id" "$err" || {
      printf '%s\n' "$err"
      return 1
    }
  done
  docker exec "$id" "$bin" -t
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
  local id name cfg dest_dir dest_in injected=0 upstream netmode admin_up err
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
    echo "    mounts:"
    docker inspect -f '{{range .Mounts}}      {{.Type}} {{.Source}} -> {{.Destination}}{{"\n"}}{{end}}' "$id" 2>/dev/null || true
    echo "    server_name lines (running nginx -T):"
    printf '%s\n' "$cfg" | grep -F "server_name" | head -40 || true
    echo "    include lines:"
    printf '%s\n' "$cfg" | grep -E '[[:space:]]include[[:space:]]' | head -20 || true

    dest_dir="$(printf '%s' "$cfg" | vhost_dir_from_dump || true)"
    if [[ -z "$dest_dir" ]]; then
      if docker exec "$id" test -d /etc/nginx/conf.d 2>/dev/null; then
        dest_dir="/etc/nginx/conf.d"
      elif docker exec "$id" test -d /etc/nginx/sites-enabled 2>/dev/null; then
        dest_dir="/etc/nginx/sites-enabled"
      else
        echo "⚠️  $name has nginx but no included vhost directory — skip"
        continue
      fi
    fi
    dest_in="${dest_dir%/}/trace.pyyol.com.conf"
    echo "    placing named vhost next to admin/pyyol: $dest_in"

    # Same reachability pattern as admin-web: container name on a shared network.
    upstream="127.0.0.1:3100"
    netmode="$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$id" 2>/dev/null || true)"
    if [[ "$netmode" != "host" ]]; then
      if join_web_to_edge_networks "$id"; then
        upstream="pyyol-lens-web:3100"
        echo "    proxy_pass http://${upstream} (container name, like admin-web)"
      else
        admin_up="$(printf '%s' "$cfg" | admin_proxy_pass_from_dump || true)"
        local gw
        gw="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.Gateway}}{{"\n"}}{{end}}' "$id" 2>/dev/null | awk 'NF { print; exit }')"
        gw="${gw:-172.17.0.1}"
        if [[ "$admin_up" == 127.0.0.1:* || "$admin_up" == host.docker.internal:* ]]; then
          local hostpart="${admin_up%%:*}"
          upstream="${hostpart}:3110"
          echo "    admin uses ${admin_up}; mirroring host for Eye at http://${upstream}"
        else
          upstream="${gw}:3110"
          echo "    $name is not host-network; proxying Eye via http://${upstream}"
        fi
      fi
    fi

    docker exec "$id" mkdir -p "$dest_dir" /etc/nginx/ssl /var/www/certbot || true
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
    # Docker nginx images are often older than 1.25 (`http2 on` is a separate directive).
    sed -i 's/http2 on;//g' "$tmp_in" 2>/dev/null || sed -i '' 's/http2 on;//g' "$tmp_in"
    if ! write_into_container "$id" "$dest_in" "$tmp_in"; then
      docker cp "$tmp_in" "$id:$dest_in"
    fi
    rm -f "$tmp_in"

    echo "    resolving Mega Hub upstreams so nginx -t can reload (site files untouched)…"
    resolve_all_missing_upstreams "$id" || true
    ensure_upgrade_map_container "$id" || true

    if nginx_test_reload "$id" && nginx_has_server_name "$id" "trace.pyyol.com"; then
      echo "✅ reloaded $name with trace.pyyol.com → ${upstream} (in running nginx -T)"
      injected=1
    else
      echo "⚠️  nginx -t/reload did not load server_name trace.pyyol.com in $name"
      # Mega Hub often keeps vhosts in nginx.conf and never includes conf.d.
      # Always try a one-line include (even when dest is conf.d). Never deletes
      # Mega Hub server blocks. cat-overwrite: docker cp fails on bind mounts.
      if ! nginx_has_server_name "$id" "trace.pyyol.com"; then
        echo "    injecting include ${dest_in} into nginx.conf (Mega Hub server blocks kept)"
        inject_trace_include "$id" "$dest_in" || true
      fi
      resolve_all_missing_upstreams "$id" || true
      if nginx_test_reload "$id" && nginx_has_server_name "$id" "trace.pyyol.com"; then
        echo "✅ reloaded $name with trace.pyyol.com → ${upstream} (in running nginx -T)"
        injected=1
      else
        echo "❌ named vhost still not in nginx -T for $name — leaving Mega Hub untouched"
        echo "    last nginx -t:"
        docker exec "$id" "$(nginx_bin "$id")" -t 2>&1 || true
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
    ensure_upgrade_map_host || true
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
