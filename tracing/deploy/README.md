# Serving Pyyol Eye at trace.pyyol.com

The dashboard container listens on **127.0.0.1:3100** (loopback) and
**0.0.0.0:3110** (dedicated port, often firewalled). It never binds :80 or :443,
so it cannot steal Mega Hub's listeners.

Mega Hub, `pyyol.com`, and `admin.pyyol.com` already share the origin nginx on
this VPS. A request for `trace.pyyol.com` is stolen by Mega Hub's default_server
until an exact `server_name trace.pyyol.com` vhost (never `default_server`)
proxies to `127.0.0.1:3100`.

That is the same pattern as `admin.pyyol.com`. Cloudflare stays orange-cloud to
origin **:443**. Sending Cloudflare to origin **:3110** only works after
Hostinger's panel firewall allows TCP 3110 (ufw on the VM is not enough); until
then that override 522s.

`deploy-tracing.yml` installs the named vhost on every tracing deploy (host
nginx and/or the shared-edge docker nginx that already has the pyyol/admin
vhosts) and fails if Host/SNI `trace.pyyol.com` still returns Mega Hub.

## Secrets

Tracing deploy: `QUERY_API_KEY`, `INGEST_API_KEY`, `PYYOL_LENS_AUTH_PASSWORD`,
`PYYOL_LENS_SESSION_SECRET`.

Optional origin-rule reconcile (same repo secrets as `dns.yml`):
`CLOUDFLARE_API_TOKEN`, optional `CLOUDFLARE_ZONE_ID`. Default is origin :443.

The dashboard login (`PYYOL_LENS_AUTH_*`) is required in production.
