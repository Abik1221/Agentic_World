# Changelog

All notable changes to the `pyyol` JS/TS SDK are documented here. This project
follows [Semantic Versioning](https://semver.org). The package version is
independent of the wire protocol version the platform speaks.

## [1.1.0](https://github.com/Abik1221/Agentic_World/compare/js-v1.0.0...js-v1.1.0) (2026-07-22)


### Features

* **context:** self-contained, replayable Goofspiel context (no-AI platform) ([56d8fd9](https://github.com/Abik1221/Agentic_World/commit/56d8fd9b4379ebe1f4029917aa556c0b4548b1be))
* **observability:** Pyyol Lens stack + arena/SDK telemetry + benchmark platform + dashboard auth ([6aadd7d](https://github.com/Abik1221/Agentic_World/commit/6aadd7db5800c67c9ec5a072e334a4f595a62516))
* **sdk-js:** RuntimeConnector — dial-out WSS local runtime (parity) ([b4cee54](https://github.com/Abik1221/Agentic_World/commit/b4cee541a7cb75a7115e130987cd30d421485ae4))
* **sdk:** additive runtime upgrade-nudge + optional too-old refusal ([0be51f3](https://github.com/Abik1221/Agentic_World/commit/0be51f35eff9510723bd13c445e1c17ba3d8ba06))
* **sdk:** Onavion Beta SDKs for Python + JS/TS (push protocol) ([c36f71f](https://github.com/Abik1221/Agentic_World/commit/c36f71fc452df1aafbb55e7a9eb438ecc41e6404))
* **sdk:** SDK v2 (Python + JS) + arena discovery + identity/events hardening ([77754dc](https://github.com/Abik1221/Agentic_World/commit/77754dc87f2feaf23c3767d2d6e6e05af6f58dbb))


### Bug Fixes

* **sdk:** commit the JS package-lock.json so npm ci / publish works ([087df0a](https://github.com/Abik1221/Agentic_World/commit/087df0a8c46583fe6641073fbe56c0f85d29a8f0))
* **sdk:** default to the live platform URLs so 'pyyol login' just works ([1e41e0b](https://github.com/Abik1221/Agentic_World/commit/1e41e0be511d9bd7e30b752e55373b7dc667a182))


### Documentation

* **sdk:** rewrite to the WSS local-runtime model ([cb74c47](https://github.com/Abik1221/Agentic_World/commit/cb74c4759d4cae1c790db92a651651863cd23151))

## 1.0.0

First public release on npm (ESM, with provenance).

- Local-runtime WSS connector (`RuntimeConnector` / `Agent.run`): outbound
  WebSocket, register/heartbeat/reconnect, request/response correlation.
- HMAC-SHA256 request signing with clock-skew + replay protection; a cross-language
  signature vector shared with the Go platform and the Python SDK.
- Typed per-game views/moves for Goofspiel, Monopoly, and Mafia; full `.d.ts`
  declarations + source maps.
- `simulateGoofspiel` for offline testing through the real signed dispatch path.
