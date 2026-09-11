# Serving Pyyol Eye at trace.pyyol.com

The dashboard container listens on **127.0.0.1:3100** (loopback) and
**0.0.0.0:3110** (dedicated public port). It never binds :80 or :443, so it
cannot block Mega Hub.

Mega Hub is the origin default on :80 of this VPS. A request for
`trace.pyyol.com` on :80 is stolen unless either:

1. host nginx has an exact `server_name trace.pyyol.com` vhost (never
   `default_server`) proxying to `127.0.0.1:3100`, or
2. Cloudflare (orange-cloud) sends that hostname to origin **port 3110**.

`deploy-tracing.yml` does both: installs the vhost when host nginx owns :80,
and upserts a Cloudflare Origin Rule `trace.pyyol.com → :3110`.

## Secrets

Tracing deploy: `QUERY_API_KEY`, `INGEST_API_KEY`, `PYYOL_LENS_AUTH_PASSWORD`,
`PYYOL_LENS_SESSION_SECRET`.

Origin-rule (same repo secrets as `dns.yml`): `CLOUDFLARE_API_TOKEN`, optional
`CLOUDFLARE_ZONE_ID`. If the token is missing, Eye is still up on :3110 and a
human must set the Cloudflare origin port (or add the nginx vhost) once.

The dashboard login (`PYYOL_LENS_AUTH_*`) is required in production.
