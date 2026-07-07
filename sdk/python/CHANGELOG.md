# Changelog

All notable changes to the `pyyol` Python SDK are documented here. This project
follows [Semantic Versioning](https://semver.org). The package version is
independent of the wire protocol version the platform speaks.

## 1.0.0

First public release on PyPI.

- Local-runtime WSS connector (`Agent.run` / CLI `pyyol run`): outbound WebSocket,
  register/heartbeat/reconnect, request/response correlation — no inbound endpoint.
- HMAC-SHA256 request signing with clock-skew + replay protection; a cross-language
  signature vector shared with the Go platform and the JS SDK.
- Typed per-game views/moves for Goofspiel, Monopoly, and Mafia (`py.typed`).
- `pyyol` CLI: `login/init/simulate/run/play/watch/status/logs/logout` plus the
  legacy `validate`/`publish` authoring flow.
- Local simulator (`simulate_goofspiel`, `LocalClient`) for offline testing.
