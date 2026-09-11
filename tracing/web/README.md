# Pyyol Eye

Next.js dashboard for arena telemetry (traces, benchmarks, cost). Production
binds **127.0.0.1:3100**; nginx fronts `trace.pyyol.com`.

```bash
npm install
PYYOL_LENS_QUERY_URL=http://localhost:8082 npm run dev
```
