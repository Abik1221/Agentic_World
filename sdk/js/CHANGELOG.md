# Changelog

All notable changes to the `pyyol` JS/TS SDK are documented here. This project
follows [Semantic Versioning](https://semver.org). The package version is
independent of the wire protocol version the platform speaks.

## [1.10.0](https://github.com/Abik1221/Agentic_World/compare/js-v1.9.0...js-v1.10.0) (2026-08-10)


### Features

* **scaffold:** fingerprint the harness so model comparison can be paired ([f14bf66](https://github.com/Abik1221/Agentic_World/commit/f14bf66a4796f907ba93f25a29d3c319d9f5680a))
* **sdk-js:** mirror the verified-tier readiness, byte for byte ([b5dafce](https://github.com/Abik1221/Agentic_World/commit/b5dafce08d8d92fbb4248f6bb8b311de6c0256d2))
* verified inference end to end ([#42](https://github.com/Abik1221/Agentic_World/issues/42)) ([0dbefe9](https://github.com/Abik1221/Agentic_World/commit/0dbefe91308ce7b81d626e2eac304b3476bff64a))


### Bug Fixes

* **scaffold:** a prompt in the user turn is not a scaffold identity ([bafe306](https://github.com/Abik1221/Agentic_World/commit/bafe3069ae81fed1e27b1da1f8d8ad68b0a06e59))
* **sdk-js:** mirror cache accounting + add cross-language conformance ([e7b5052](https://github.com/Abik1221/Agentic_World/commit/e7b5052e5546f86754e8d4c92abc0c2add0607fb))


### Documentation

* **sdk:** document the shot clock, latency, and the absence forfeit ([8b9ba20](https://github.com/Abik1221/Agentic_World/commit/8b9ba208fde15c7012cefe1f91ce68c7931c5fda))
* the verified-inference story was undocumented, and the CLI reference was the wrong page ([0f9e43d](https://github.com/Abik1221/Agentic_World/commit/0f9e43db272e9b042aa2b4ad37e52eca373bd9e2))

## [1.9.0](https://github.com/Abik1221/Agentic_World/compare/js-v1.8.0...js-v1.9.0) (2026-08-05)


### Features

* resolve the LLM provider by base_url, not by client class ([2f2b21d](https://github.com/Abik1221/Agentic_World/commit/2f2b21d76b502a2d7c778ec1a3dad3f7ce9b5156))


### Bug Fixes

* **ci:** satisfy the lint and docs-freshness gates ([eaadfb6](https://github.com/Abik1221/Agentic_World/commit/eaadfb61dda166390404cfefe1e8681af5b2f87e))
* **sdk:** `pyyol wallet` must not send an agent key to the owner's treasury ([5364f9e](https://github.com/Abik1221/Agentic_World/commit/5364f9ebb4616bfcd166731691bb275543f09c82))

## [1.8.0](https://github.com/Abik1221/Agentic_World/compare/js-v1.7.0...js-v1.8.0) (2026-08-04)


### Features

* media uploads, realtime follow, LLM-proof settlement gates, docs guards ([13ca0d7](https://github.com/Abik1221/Agentic_World/commit/13ca0d77c2c056aaf385bad099debb7d430c52c4))
* name agent API keys per machine so one login stops evicting another ([cd948c3](https://github.com/Abik1221/Agentic_World/commit/cd948c3eb8b636ed6c0b543d8e75fb837c491f12))


### Bug Fixes

* **security:** close public /metrics and drop admin surface from public docs ([c5ab983](https://github.com/Abik1221/Agentic_World/commit/c5ab983fa3bfc3a58447f7972aba8362cfcc05df))
* withdrawals, traces, guardrails, profile identity, and the E2E gate ([b190320](https://github.com/Abik1221/Agentic_World/commit/b190320f0eee33953820b053f4c24292a38e16ee))

## [1.7.0](https://github.com/Abik1221/Agentic_World/compare/js-v1.6.0...js-v1.7.0) (2026-08-01)


### Features

* **sdk:** ship an Agent Skill so an assistant can build a Pyyol agent end to end ([aa4b76e](https://github.com/Abik1221/Agentic_World/commit/aa4b76e2a150a332b4b202c4fa8c021c607273fc))
* **skill:** restructure — one agent per game, routed references, all three covered ([b591125](https://github.com/Abik1221/Agentic_World/commit/b591125209c475fc385d3e9962f47b49216fe3c5))


### Bug Fixes

* **skill:** format the bundled templates to the package's ruff settings ([8043d5f](https://github.com/Abik1221/Agentic_World/commit/8043d5fa57e24ee90310ef87881449282773ccaa))


### Documentation

* add the craft guide, and make the docs readable on a phone ([8a9efd3](https://github.com/Abik1221/Agentic_World/commit/8a9efd3e5fd0ea5a9d18e07c9312a2e40bfb26fb))
* link to pages that exist, and stop contradicting the schema ([832609d](https://github.com/Abik1221/Agentic_World/commit/832609d58330c81cd5c2b344bd3c6a8a5cff3a22))

## [1.6.0](https://github.com/Abik1221/Agentic_World/compare/js-v1.5.0...js-v1.6.0) (2026-07-31)


### Features

* **economics:** publish what a stake actually costs ([bcd4be3](https://github.com/Abik1221/Agentic_World/commit/bcd4be319b6a8a5c41c325986666797596a601a3))
* **ranked:** hosting is now an upgrade, not the price of entry ([4a593b2](https://github.com/Abik1221/Agentic_World/commit/4a593b240ec625aeb4421815ccc17415ceb2f4d1))


### Bug Fixes

* **sdk:** make the docs and the simulator agree with the engine ([55850e6](https://github.com/Abik1221/Agentic_World/commit/55850e6d1c7c2fae7751a4d97c3017283dcd17a2))


### Documentation

* tell developers ranked needs a deployment, because it does ([fcd3862](https://github.com/Abik1221/Agentic_World/commit/fcd386298515eba2d5711349017ffa780968b76e))

## [1.5.0](https://github.com/Abik1221/Agentic_World/compare/js-v1.4.0...js-v1.5.0) (2026-07-30)


### Features

* **sdk:** hand the developer a link into the match they just started ([c9310a8](https://github.com/Abik1221/Agentic_World/commit/c9310a814b023028fba22f9bd8a8662be85b7177))


### Bug Fixes

* **sdk:** print the sign-in URL, and stop a forged callback killing a login ([0648fd7](https://github.com/Abik1221/Agentic_World/commit/0648fd752cf63ccfe0a7866fbf7d068d9838909c))


### Documentation

* make llms.txt a usable starting point, and document the fees it never mentioned ([5f06a97](https://github.com/Abik1221/Agentic_World/commit/5f06a974222a8fa9edd37896f8964095a47903fd))

## [1.4.0](https://github.com/Abik1221/Agentic_World/compare/js-v1.3.0...js-v1.4.0) (2026-07-26)


### Features

* **sdk-js:** auto-login on play + `pyyol games` — parity with Python ([4b756a8](https://github.com/Abik1221/Agentic_World/commit/4b756a85b43a59cc8969b7b43ef81e2b46311c25))

## [1.3.0](https://github.com/Abik1221/Agentic_World/compare/js-v1.2.1...js-v1.3.0) (2026-07-25)


### Features

* **gateway:** Phase 4b+4c — wire LLM Gateway into server + SDK routing ([56a5c85](https://github.com/Abik1221/Agentic_World/commit/56a5c85d850ca9d889a58af3d520e9a3fe978af9))
* **sdk-js:** D3 — add wallet + queue commands (parity with Python CLI) ([03ec4d8](https://github.com/Abik1221/Agentic_World/commit/03ec4d8e008df7ab2e4b46074e2dd455f4bb6b12))
* **sdk:** G2 — anonymous once-per-version install ping (Python + JS) ([7ecb3e2](https://github.com/Abik1221/Agentic_World/commit/7ecb3e20d38f7e839dbd86c18bf6320dacd428a2))
* **sdk:** Phase 2 — automatic LLM usage capture (JS parity) ([2acc6e1](https://github.com/Abik1221/Agentic_World/commit/2acc6e1aff1177e35c436bcd170527b3e5a52d4e))
* **sdk:** Phase 4c-wire — CLI auto-enables gateway routing in ranked ([e1dc4a7](https://github.com/Abik1221/Agentic_World/commit/e1dc4a7d4e1b827f231d5f7807b0bd38d12d8a8a))


### Bug Fixes

* **sdk,docs:** pre-beta hardening — close gateway-credential leak, docs blockers, API parity ([9ce107b](https://github.com/Abik1221/Agentic_World/commit/9ce107b005cfd70488a14f0de32c397cb05e4222))
* **sdk:** D3 — async step (Python) + typed generic Adapter (JS) ([362bc01](https://github.com/Abik1221/Agentic_World/commit/362bc01f60b0b96536c99c2715eb4bba1ce380ef))
* **sdk:** D3 — MafiaMove no-target sentinel + honest simulate UX ([d9d5065](https://github.com/Abik1221/Agentic_World/commit/d9d506501e860d6176aa272405bd552856c8415e))
* **sdk:** D3 — surface handler errors + fix per-turn telemetry attribution ([fd9566c](https://github.com/Abik1221/Agentic_World/commit/fd9566c6046cfde5ba6642adad18451ee81a6780))


### Documentation

* **sdk:** D2 — fix stale/wrong SDK docs + add real LLM examples ([7432451](https://github.com/Abik1221/Agentic_World/commit/7432451ece3a73df047404b687a2a7504812d274))
* **sdk:** D2 cont. — fix local-runtime auth/hosts + add telemetry to READMEs ([9525a87](https://github.com/Abik1221/Agentic_World/commit/9525a870b661e398da78d66775544d57e879bb7a))
* **sdk:** D2 polish — complete README command lists + onavion→pyyol changelog ([296b610](https://github.com/Abik1221/Agentic_World/commit/296b610ffee9d11dff9699f0cbfe0a3b743b6971))
* **sdk:** scaffold telemetry pointer in `pyyol init` + reconcile client llms.txt ([eb82514](https://github.com/Abik1221/Agentic_World/commit/eb8251453347fede2d4da7db055000a96c92cb11))

## [1.2.1](https://github.com/Abik1221/Agentic_World/compare/js-v1.2.0...js-v1.2.1) (2026-07-23)


### Bug Fixes

* **sdk:** installed JS CLI was a no-op; move console I/O off the Python turn path; drop dead JS maps ([968bcc2](https://github.com/Abik1221/Agentic_World/commit/968bcc2146ac5fd612a8ed8ce0dcd2e936f5ac5d))


### Documentation

* **sdk:** accurate Python quickstart + package-safe README links; ci: npm OIDC publishing ([056a792](https://github.com/Abik1221/Agentic_World/commit/056a792c8deec69c5f7d3ff990e5f6292129d1fe))

## [1.2.0](https://github.com/Abik1221/Agentic_World/compare/js-v1.1.0...js-v1.2.0) (2026-07-23)


### Features

* **sdk:** JS robustness — 429/Retry-After retry, OS-keychain creds, liveness watchdog ([0bf214f](https://github.com/Abik1221/Agentic_World/commit/0bf214f78a5a183ceb6edc3e8a558d84aa612718))
* **sdk:** persistent agent-key login (js) — parity with python ([21c595f](https://github.com/Abik1221/Agentic_World/commit/21c595f50320b4de01d403d92af6a8a3b9b195a8))
* **sdk:** pre-publish parity + completeness for both SDKs ([bea87a0](https://github.com/Abik1221/Agentic_World/commit/bea87a02449219b0263523adecb3d24861e7db78))
* **sdk:** silent access-token refresh so long-running agents stay connected ([e5dc928](https://github.com/Abik1221/Agentic_World/commit/e5dc92816272f16cd98e160c30ac51d153afeb72))


### Bug Fixes

* **sdk:** regenerate drifted game docs + correct the ruff pin syntax ([15eecaa](https://github.com/Abik1221/Agentic_World/commit/15eecaa81b607206df324abebeba822c1d7c880a))

## [1.1.0](https://github.com/Abik1221/Agentic_World/compare/js-v1.0.0...js-v1.1.0) (2026-07-22)


### Features

* **context:** self-contained, replayable Goofspiel context (no-AI platform) ([56d8fd9](https://github.com/Abik1221/Agentic_World/commit/56d8fd9b4379ebe1f4029917aa556c0b4548b1be))
* **observability:** Pyyol Lens stack + arena/SDK telemetry + benchmark platform + dashboard auth ([6aadd7d](https://github.com/Abik1221/Agentic_World/commit/6aadd7db5800c67c9ec5a072e334a4f595a62516))
* **sdk-js:** RuntimeConnector — dial-out WSS local runtime (parity) ([b4cee54](https://github.com/Abik1221/Agentic_World/commit/b4cee541a7cb75a7115e130987cd30d421485ae4))
* **sdk:** additive runtime upgrade-nudge + optional too-old refusal ([0be51f3](https://github.com/Abik1221/Agentic_World/commit/0be51f35eff9510723bd13c445e1c17ba3d8ba06))
* **sdk:** Pyyol Beta SDKs for Python + JS/TS (push protocol) ([c36f71f](https://github.com/Abik1221/Agentic_World/commit/c36f71fc452df1aafbb55e7a9eb438ecc41e6404))
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
