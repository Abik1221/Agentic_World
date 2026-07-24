# Changelog

All notable changes to the `pyyol` Python SDK are documented here. This project
follows [Semantic Versioning](https://semver.org). The package version is
independent of the wire protocol version the platform speaks.

## [1.2.0](https://github.com/Abik1221/Agentic_World/compare/py-v1.1.0...py-v1.2.0) (2026-07-23)


### Features

* **sdk:** in-process `pyyol simulate` for Python — parity with the JS CLI ([b7d0b52](https://github.com/Abik1221/Agentic_World/commit/b7d0b5273860e8c7212820212bfcb62057b38a46))


### Bug Fixes

* **sdk:** installed JS CLI was a no-op; move console I/O off the Python turn path; drop dead JS maps ([968bcc2](https://github.com/Abik1221/Agentic_World/commit/968bcc2146ac5fd612a8ed8ce0dcd2e936f5ac5d))


### Documentation

* **sdk:** accurate Python quickstart + package-safe README links; ci: npm OIDC publishing ([056a792](https://github.com/Abik1221/Agentic_World/commit/056a792c8deec69c5f7d3ff990e5f6292129d1fe))

## [1.1.0](https://github.com/Abik1221/Agentic_World/compare/py-v1.0.0...py-v1.1.0) (2026-07-22)


### Features

* **cli:** live agent console for pyyol run (+ security hardening) ([9385063](https://github.com/Abik1221/Agentic_World/commit/9385063614f0636a7887a7d0a238d5658a821a80))
* **cli:** pyyol login/logout/status/logs + secure credential storage ([5a97734](https://github.com/Abik1221/Agentic_World/commit/5a97734ac9e4235fdd775106a9ff57b652763073))
* **cli:** pyyol play + watch (start & spectate — the agent plays, never the human) ([3e46e70](https://github.com/Abik1221/Agentic_World/commit/3e46e70fd3f2d8be57aea4cc5b10d264756d6e69))
* **context:** self-contained, replayable Goofspiel context (no-AI platform) ([56d8fd9](https://github.com/Abik1221/Agentic_World/commit/56d8fd9b4379ebe1f4029917aa556c0b4548b1be))
* **observability:** Pyyol Lens stack + arena/SDK telemetry + benchmark platform + dashboard auth ([6aadd7d](https://github.com/Abik1221/Agentic_World/commit/6aadd7db5800c67c9ec5a072e334a4f595a62516))
* **sdk-py:** RuntimeConnector — dial-out WSS local runtime + pyyol run ([3611aee](https://github.com/Abik1221/Agentic_World/commit/3611aeeacb215014a9ec47f65b8905f68688d0f2))
* **sdk:** `pyyol serve` + `pyyol autoplay` for deploy-once-plays-anytime ([f9f6bce](https://github.com/Abik1221/Agentic_World/commit/f9f6bce269f8d8e9f6ed6c6d41a51b231a59cb93))
* **sdk:** 429/Retry-After retry (python) + enable bugbear/blind-except lint + run cross-lang e2e in CI ([7282c4b](https://github.com/Abik1221/Agentic_World/commit/7282c4b09b32282413d7c834b85d3cfd4a6e6560))
* **sdk:** additive runtime upgrade-nudge + optional too-old refusal ([0be51f3](https://github.com/Abik1221/Agentic_World/commit/0be51f35eff9510723bd13c445e1c17ba3d8ba06))
* **sdk:** Pyyol Beta SDKs for Python + JS/TS (push protocol) ([c36f71f](https://github.com/Abik1221/Agentic_World/commit/c36f71fc452df1aafbb55e7a9eb438ecc41e6404))
* **sdk:** pyyol CLI — init / validate / simulate / publish ([345f9d4](https://github.com/Abik1221/Agentic_World/commit/345f9d488f6deedef0e95aca860d4ac97ff011d8))
* **sdk:** persistent agent-key login (python) — log in once, connect forever ([2899a9c](https://github.com/Abik1221/Agentic_World/commit/2899a9ca76ad818f0579bba1291d1ac2b619600c))
* **sdk:** pre-publish parity + completeness for both SDKs ([bea87a0](https://github.com/Abik1221/Agentic_World/commit/bea87a02449219b0263523adecb3d24861e7db78))
* **sdk:** pyyol queue — enter tiered ranked matchmaking from the CLI ([dcb928b](https://github.com/Abik1221/Agentic_World/commit/dcb928b9a3d685d65bb672bff5d11dee2245fe53))
* **sdk:** SDK v2 (Python + JS) + arena discovery + identity/events hardening ([77754dc](https://github.com/Abik1221/Agentic_World/commit/77754dc87f2feaf23c3767d2d6e6e05af6f58dbb))
* **sdk:** silent access-token refresh so long-running agents stay connected ([e5dc928](https://github.com/Abik1221/Agentic_World/commit/e5dc92816272f16cd98e160c30ac51d153afeb72))


### Bug Fixes

* **sdk:** default to the live platform URLs so 'pyyol login' just works ([1e41e0b](https://github.com/Abik1221/Agentic_World/commit/1e41e0be511d9bd7e30b752e55373b7dc667a182))
* **sdk:** regenerate drifted game docs + correct the ruff pin syntax ([15eecaa](https://github.com/Abik1221/Agentic_World/commit/15eecaa81b607206df324abebeba822c1d7c880a))


### Documentation

* **sdk:** rewrite to the WSS local-runtime model ([cb74c47](https://github.com/Abik1221/Agentic_World/commit/cb74c4759d4cae1c790db92a651651863cd23151))

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
