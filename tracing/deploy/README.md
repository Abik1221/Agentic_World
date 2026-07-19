# Serving Pyyol Lens at trace.pyyol.com

The observability UI runs on a subdomain of the main product. The main user app
(`pyyol.com`) and the admin app live in their own repos; **tracing is hosted here**
on `trace.pyyol.com`.

Only the `web` service is exposed. It talks to `query-api` / `control-api`
server-side over the compose network, so nothing else needs a public port.

## One-time server setup

1. **DNS** — add an `A` record `trace.pyyol.com` → the VPS IP.
2. **Secrets** — `cp deploy/.env.trace.example deploy/.env.trace` and fill in
   `PYYOL_LENS_AUTH_PASSWORD` + `PYYOL_LENS_SESSION_SECRET` (the subdomain is public,
   so the dashboard login is mandatory). Keep `deploy/.env.trace` out of git.
3. **Bring up the stack** (from `tracing/`):
   ```bash
   docker compose -f docker-compose.yml -f deploy/docker-compose.trace.yml \
     --env-file deploy/.env.trace up -d --build
   ```
   The web UI now listens on `127.0.0.1:3100` (loopback only).
4. **Reverse proxy + TLS**:
   ```bash
   cp deploy/nginx/trace.pyyol.com.conf /etc/nginx/sites-available/
   ln -s /etc/nginx/sites-available/trace.pyyol.com.conf /etc/nginx/sites-enabled/
   certbot --nginx -d trace.pyyol.com
   nginx -t && systemctl reload nginx
   ```

`https://trace.pyyol.com` is then live, behind the dashboard login.

## Wiring the backend to emit here

The Agent Arena backend ships telemetry when `PYYOL_LENS_ENABLED=true` +
`PYYOL_LENS_ENDPOINT` points at the ingest API (`:8081`, kept private — do NOT put
ingest on the public subdomain). Set those via the backend deploy secrets
(`PYYOL_LENS_ENDPOINT`, `PYYOL_LENS_API_KEY`) — see `.github/workflows/deploy-backend.yml`.
