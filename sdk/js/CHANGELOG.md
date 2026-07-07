# Changelog

All notable changes to the `pyyol` JS/TS SDK are documented here. This project
follows [Semantic Versioning](https://semver.org). The package version is
independent of the wire protocol version the platform speaks.

## 1.0.0

First public release on npm (ESM, with provenance).

- Local-runtime WSS connector (`RuntimeConnector` / `Agent.run`): outbound
  WebSocket, register/heartbeat/reconnect, request/response correlation.
- HMAC-SHA256 request signing with clock-skew + replay protection; a cross-language
  signature vector shared with the Go platform and the Python SDK.
- Typed per-game views/moves for Goofspiel, Monopoly, and Mafia; full `.d.ts`
  declarations + source maps.
- `simulateGoofspiel` for offline testing through the real signed dispatch path.
