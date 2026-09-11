# Serving Pyyol Eye at trace.pyyol.com

The telemetry UI runs on a subdomain of the main product. `pyyol.com` and the
admin app live in their own repos; **Pyyol Eye is hosted here** on `trace.pyyol.com`.

The dashboard container listens on **127.0.0.1:3100** (loopback only) so it never
occupies :80/:443 and cannot block Mega Hub or the other Pyyol vhosts. Host nginx
must have an exact `server_name trace.pyyol.com` vhost — without it, Mega Hub is
the origin `default_server` and steals this Host header.

`deploy-tracing.yml` installs that vhost on every deploy via
`deploy/install-trace-vhost.sh`. It never sets `default_server`.

## One-time notes

1. **DNS** — `A` record `trace.pyyol.com` → the VPS IP (Cloudflare orange-cloud is fine).
2. **Secrets** — tracing deploy secrets (`QUERY_API_KEY`, `INGEST_API_KEY`,
   `PYYOL_LENS_AUTH_PASSWORD`, `PYYOL_LENS_SESSION_SECRET`).
3. **TLS** — the install script uses Let's Encrypt when certbot can issue;
   otherwise a self-signed origin cert so SNI matches (needed if Cloudflare SSL
   is Full). Full Strict needs a real cert; re-run deploy or `certbot certonly --webroot`.

The dashboard login (`PYYOL_LENS_AUTH_*`) is required in production.
