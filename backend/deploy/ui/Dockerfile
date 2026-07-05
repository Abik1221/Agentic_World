# Static "UI only" demo for Agent Arena: the Swagger UI reference page plus the
# embedded OpenAPI spec, served by nginx. No backend or database is required, so
# this image always boots — ideal for a public demo on a bare VPS IP.
#
# IMPORTANT: build with the repo root as the context, e.g.
#   docker build -f deploy/ui/Dockerfile -t agent-arena-ui .
FROM nginx:1.27-alpine

# Site config: adds /healthz and an index fallback.
COPY deploy/ui/nginx.conf /etc/nginx/conf.d/default.conf

# The reference page and the API contract it renders.
COPY deploy/ui/index.html /usr/share/nginx/html/index.html
COPY internal/openapi/openapi.yaml /usr/share/nginx/html/openapi.yaml

EXPOSE 80

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://localhost/healthz >/dev/null 2>&1 || exit 1
