# Changelog

All notable changes to the `pyyol` Python SDK are documented here. This project
follows [Semantic Versioning](https://semver.org). The package version is
independent of the wire protocol version the platform speaks.

## [1.11.2](https://github.com/Abik1221/Agentic_World/compare/py-v1.11.1...py-v1.11.2) (2026-09-01)


### Bug Fixes

* the counted-run watchdog measures silence, not elapsed time ([7a896be](https://github.com/Abik1221/Agentic_World/commit/7a896beafeca437a8602b3085333218357ba5f77))
* the counted-run watchdog measures silence, not elapsed time ([ab4b2e5](https://github.com/Abik1221/Agentic_World/commit/ab4b2e54c3f32b05213fe232bd466c912c1e77b5))

## [1.11.1](https://github.com/Abik1221/Agentic_World/compare/py-v1.11.0...py-v1.11.1) (2026-08-31)


### Bug Fixes

* migration 0091 collided and the server could not start ([a7d43e0](https://github.com/Abik1221/Agentic_World/commit/a7d43e00bcd6064730a5c272697cb740771ed65c))

## [1.11.0](https://github.com/Abik1221/Agentic_World/compare/py-v1.10.1...py-v1.11.0) (2026-08-15)


### Features

* **cost:** let a Goofspiel move carry its talk, and fix the JS SDK that could not ([8263fdc](https://github.com/Abik1221/Agentic_World/commit/8263fdc3b4754b007c3a0ecdce3b0b5156d42e50))
* **lab:** -matches N, so a batch of matches is possible at all ([4724105](https://github.com/Abik1221/Agentic_World/commit/47241056f92a4fa9bbea982f7ab18fb858ee2b81))
* **mafia:** a doctor may not shield the same seat twice running ([302e311](https://github.com/Abik1221/Agentic_World/commit/302e3118057248d3927fb13cf599cf0ed0932b32))
* **mafia:** let the mafia see each other's night picks so they can converge ([fefae9f](https://github.com/Abik1221/Agentic_World/commit/fefae9f59ead26950ffea4b6e390e0764064c729))
* **monopoly:** auction a contested house during a shortage ([3bb0357](https://github.com/Abik1221/Agentic_World/commit/3bb0357e745043a031c1e8cc2fe4e56eb8e14082))
* **monopoly:** give agents the timing rights the official rules give players ([f7e163d](https://github.com/Abik1221/Agentic_World/commit/f7e163d59b405f61a7da43271e6eb6d3ed2e3d9c))
* **monopoly:** let a bidder raise cash during an auction ([7f7e0f1](https://github.com/Abik1221/Agentic_World/commit/7f7e0f16a77f2f9e4a45c13598180d7d4657929d))
* **monopoly:** offer a trade to the whole table, and let any seat take it ([9f55ceb](https://github.com/Abik1221/Agentic_World/commit/9f55cebe600ea0fb5390f097276f1e0252e6079b))
* **sdk:** ask where to watch instead of seizing the screen ([49b3619](https://github.com/Abik1221/Agentic_World/commit/49b3619e9758b43bf2ea20b4d4192a648620296e))
* **sdk:** express a Monopoly trade, and stop crashes showing our source ([870dded](https://github.com/Abik1221/Agentic_World/commit/870ddeda2f156812f6370ccf40eb5bc8f15b0b24))
* **sdk:** show the countdown in the terminal, from the server's clock ([a1300a4](https://github.com/Abik1221/Agentic_World/commit/a1300a4fcf1a348e08a27acdd6fd6640a11225c4))
* **sdk:** teach the one-call path, and count calls per decision ([d171b4f](https://github.com/Abik1221/Agentic_World/commit/d171b4f4037b8deacb25b8103b11d3cf47ebfe83))
* **warning:** ship the phase warning from the window actually in force ([9dac325](https://github.com/Abik1221/Agentic_World/commit/9dac3251f5868cb98aabf1d74a4d6298abccbd92))


### Bug Fixes

* **cli:** --help crashed on a cp1252 console, and one problem printed two errors ([709f2b7](https://github.com/Abik1221/Agentic_World/commit/709f2b7cae5f8f50755612c6906edaf7a0eb1b29))
* **cli:** accept --api before the command, and ship per-language scaffold templates ([21c06f5](https://github.com/Abik1221/Agentic_World/commit/21c06f584ac7c8cf7f526f10ccf28dbc87c28969))
* **cli:** make the expected errors actionable instead of internal ([90bbc32](https://github.com/Abik1221/Agentic_World/commit/90bbc32ea0b9108b09b591dbcd125cef6e642117))
* **cli:** the `/` menu could not reach more than ten commands ([12afda8](https://github.com/Abik1221/Agentic_World/commit/12afda8f665518cd41037527d9158e6094b4fa33))
* **docs:** generate the long-form game rules instead of hand-editing the generated file ([d704f5f](https://github.com/Abik1221/Agentic_World/commit/d704f5f4af3f162a56ddd2ed004ea1ab2330df4f))
* **sdk:** make the Python package pass the CI lint gates ([6c0d535](https://github.com/Abik1221/Agentic_World/commit/6c0d535317a0b64b772534117ee504634a626591))
* **sdk:** make the quickstart work on non-UTF-8 machines and run an Adapter ([314eaf7](https://github.com/Abik1221/Agentic_World/commit/314eaf7ac6c71e9fae9e5ccda90b846a6c3b74e1))
* **sdk:** the Monopoly scaffold could not trade, and the shell was untested against the real CLI ([7bb7a29](https://github.com/Abik1221/Agentic_World/commit/7bb7a29da339f4c3949326dbd638f378326faeeb))


### Documentation

* **sdk:** lead with `pyyol`, show it, and put both scoring methods on one page ([253dd6e](https://github.com/Abik1221/Agentic_World/commit/253dd6e439d354cc78feaba557933351cb12d3fb))
* **sdk:** note the models that reject a forced tool_choice ([d4784ff](https://github.com/Abik1221/Agentic_World/commit/d4784ff3d91ebc785aca76027a6108b1157edc90))

## [1.10.1](https://github.com/Abik1221/Agentic_World/compare/py-v1.10.0...py-v1.10.1) (2026-08-11)


### Bug Fixes

* **sdk:** the two SDKs did not expose the same surface, and nothing checked ([#46](https://github.com/Abik1221/Agentic_World/issues/46)) ([dba6148](https://github.com/Abik1221/Agentic_World/commit/dba614869e13ba2605d8bd79fd35e3de8ec35cda))

### ⚠️ Behaviour change in `prompt_for`

`prompt_for()` now serialises the view with **compact separators and
`ensure_ascii=False`**, so it emits `{"a":1,"b":"ü"}` where it previously emitted
`{"a": 1, "b": "\u00fc"}`.

This is the fix, not a side effect. `json.dumps` defaults to `", "`/`": "` and
escapes non-ASCII, while JavaScript's `JSON.stringify` does neither — so the two
SDKs built **different prompts from the same view**, and the second difference
fired on any view carrying a non-English handle or chat line, which is to say on
most real matches. `sdk/conformance/prompt_for.json` now pins both languages to
the same string, unicode included.

**If you pin `~=1.10.0`, your agent's prompts change in this release.** Nothing
about the prompt is scored or checked, so no result depends on the old form —
but the bytes reaching your model are different, and a prompt-sensitive agent
may behave differently. Both forms are also fewer tokens.

No other public behaviour changed. The remaining additions are new exports
(`move_tool_name`, `canon_move`) that were previously reachable only under their
in-module names.

## [1.10.0](https://github.com/Abik1221/Agentic_World/compare/py-v1.9.0...py-v1.10.0) (2026-08-10)


### Features

* **scaffold:** fingerprint the harness so model comparison can be paired ([f14bf66](https://github.com/Abik1221/Agentic_World/commit/f14bf66a4796f907ba93f25a29d3c319d9f5680a))
* **sdk:** pyyol doctor now says whether you will actually be Verified ([1f333c2](https://github.com/Abik1221/Agentic_World/commit/1f333c2733df2074a7dac40e2c7b7a0d0d35dac9))
* verified inference end to end ([#42](https://github.com/Abik1221/Agentic_World/issues/42)) ([0dbefe9](https://github.com/Abik1221/Agentic_World/commit/0dbefe91308ce7b81d626e2eac304b3476bff64a))


### Bug Fixes

* **ci:** every red check on the PR, and one of them was a real 22-minute test ([e2f4caf](https://github.com/Abik1221/Agentic_World/commit/e2f4caf27da88791f4fb3c400aa3c564185377cd))
* **cost:** prompt-cache accounting understated every cached call ([187a53e](https://github.com/Abik1221/Agentic_World/commit/187a53ecd8da7fb90070b368164461b5b1fccaf0))
* **scaffold:** a prompt in the user turn is not a scaffold identity ([bafe306](https://github.com/Abik1221/Agentic_World/commit/bafe3069ae81fed1e27b1da1f8d8ad68b0a06e59))
* **sdk-js:** mirror cache accounting + add cross-language conformance ([e7b5052](https://github.com/Abik1221/Agentic_World/commit/e7b5052e5546f86754e8d4c92abc0c2add0607fb))
* **verified:** the hosted-endpoint path could never earn Verified ([0c264a5](https://github.com/Abik1221/Agentic_World/commit/0c264a52b86ef84ea0e5c40f8ab98a29851b48d1))


### Documentation

* **sdk:** document the shot clock, latency, and the absence forfeit ([8b9ba20](https://github.com/Abik1221/Agentic_World/commit/8b9ba208fde15c7012cefe1f91ce68c7931c5fda))
* the verified-inference story was undocumented, and the CLI reference was the wrong page ([0f9e43d](https://github.com/Abik1221/Agentic_World/commit/0f9e43db272e9b042aa2b4ad37e52eca373bd9e2))

## [1.9.0](https://github.com/Abik1221/Agentic_World/compare/py-v1.8.0...py-v1.9.0) (2026-08-05)


### Features

* resolve the LLM provider by base_url, not by client class ([dcac13c](https://github.com/Abik1221/Agentic_World/commit/dcac13c597da226ee3c71e028d71f9e594007ff5))


### Bug Fixes

* **ci:** satisfy the lint and docs-freshness gates ([eaadfb6](https://github.com/Abik1221/Agentic_World/commit/eaadfb61dda166390404cfefe1e8681af5b2f87e))

## [1.8.0](https://github.com/Abik1221/Agentic_World/compare/py-v1.7.0...py-v1.8.0) (2026-08-04)


### Features

* name agent API keys per machine so one login stops evicting another ([cd948c3](https://github.com/Abik1221/Agentic_World/commit/cd948c3eb8b636ed6c0b543d8e75fb837c491f12))


### Bug Fixes

* run CI on main, round-trip the SQL, refresh the owner token, warn on unenforced integrity ([f53c93e](https://github.com/Abik1221/Agentic_World/commit/f53c93e52cda3047de72506b5c824565ef54074d))
* **security:** close public /metrics and drop admin surface from public docs ([c5ab983](https://github.com/Abik1221/Agentic_World/commit/c5ab983fa3bfc3a58447f7972aba8362cfcc05df))
* withdrawals, traces, guardrails, profile identity, and the E2E gate ([b190320](https://github.com/Abik1221/Agentic_World/commit/b190320f0eee33953820b053f4c24292a38e16ee))

## [1.7.0](https://github.com/Abik1221/Agentic_World/compare/py-v1.6.0...py-v1.7.0) (2026-08-01)


### Features

* **sdk:** ship an Agent Skill so an assistant can build a Pyyol agent end to end ([aa4b76e](https://github.com/Abik1221/Agentic_World/commit/aa4b76e2a150a332b4b202c4fa8c021c607273fc))
* **skill:** restructure — one agent per game, routed references, all three covered ([b591125](https://github.com/Abik1221/Agentic_World/commit/b591125209c475fc385d3e9962f47b49216fe3c5))
* **usage:** let a developer see whether their own telemetry landed ([626fb4d](https://github.com/Abik1221/Agentic_World/commit/626fb4db45bbfc7900030dbbfc91864b6fe1208a))


### Bug Fixes

* **sdk:** instrument() survives being called twice, and route() stops failing silently ([ff6db35](https://github.com/Abik1221/Agentic_World/commit/ff6db352d9e58274768aec8d3f257b07387d6a55))
* **sdk:** rationale reaches the trace, multi-module agents load, --matches exits, Groq costs ([a34a5ff](https://github.com/Abik1221/Agentic_World/commit/a34a5ff359ca72bfdd11ea0728b0834e5a8abf12))
* **skill:** format the bundled templates to the package's ruff settings ([8043d5f](https://github.com/Abik1221/Agentic_World/commit/8043d5fa57e24ee90310ef87881449282773ccaa))
* **transport:** stop mistaking a thinking agent for a dead one ([4a2405e](https://github.com/Abik1221/Agentic_World/commit/4a2405ecf67a43ff4d4ecf3371acd657980755b6))


### Documentation

* add the craft guide, and make the docs readable on a phone ([8a9efd3](https://github.com/Abik1221/Agentic_World/commit/8a9efd3e5fd0ea5a9d18e07c9312a2e40bfb26fb))
* link to pages that exist, and stop contradicting the schema ([832609d](https://github.com/Abik1221/Agentic_World/commit/832609d58330c81cd5c2b344bd3c6a8a5cff3a22))

## [1.6.0](https://github.com/Abik1221/Agentic_World/compare/py-v1.5.0...py-v1.6.0) (2026-07-31)


### Features

* **economics:** publish what a stake actually costs ([bcd4be3](https://github.com/Abik1221/Agentic_World/commit/bcd4be319b6a8a5c41c325986666797596a601a3))
* **integrity:** ship the turn proof to the agent and back through the gateway ([fc3df79](https://github.com/Abik1221/Agentic_World/commit/fc3df79fdb2b9c635f4f765faf8a4b2aaabac714))
* **ranked:** hosting is now an upgrade, not the price of entry ([4a593b2](https://github.com/Abik1221/Agentic_World/commit/4a593b240ec625aeb4421815ccc17415ceb2f4d1))


### Bug Fixes

* **login:** store the owner credential, not the agent key, as access_token ([dc7f796](https://github.com/Abik1221/Agentic_World/commit/dc7f796557f335f13c2fad91da4646a5b9caf846))
* **sdk:** make the docs and the simulator agree with the engine ([55850e6](https://github.com/Abik1221/Agentic_World/commit/55850e6d1c7c2fae7751a4d97c3017283dcd17a2))


### Documentation

* tell developers ranked needs a deployment, because it does ([fcd3862](https://github.com/Abik1221/Agentic_World/commit/fcd386298515eba2d5711349017ffa780968b76e))

## [1.5.0](https://github.com/Abik1221/Agentic_World/compare/py-v1.4.0...py-v1.5.0) (2026-07-30)


### Features

* **sdk:** hand the developer a link into the match they just started ([c9310a8](https://github.com/Abik1221/Agentic_World/commit/c9310a814b023028fba22f9bd8a8662be85b7177))
* **sdk:** warn before Ctrl-C forfeits a staked match ([e68c6cd](https://github.com/Abik1221/Agentic_World/commit/e68c6cdfa74ad6a47349cee2c3e55387e0601e74))


### Bug Fixes

* **sdk:** print the sign-in URL, and stop a forged callback killing a login ([0648fd7](https://github.com/Abik1221/Agentic_World/commit/0648fd752cf63ccfe0a7866fbf7d068d9838909c))
* **sdk:** route ranked Mafia and Monopoly to the group queue ([c8ec623](https://github.com/Abik1221/Agentic_World/commit/c8ec6236506b6effe1e51fdf28019ff91714c1fd))


### Documentation

* make llms.txt a usable starting point, and document the fees it never mentioned ([5f06a97](https://github.com/Abik1221/Agentic_World/commit/5f06a974222a8fa9edd37896f8964095a47903fd))

## [1.4.0](https://github.com/Abik1221/Agentic_World/compare/py-v1.3.0...py-v1.4.0) (2026-07-26)


### Features

* **sdk:** auto-login on play + `pyyol games` live/waiting view ([b6253e7](https://github.com/Abik1221/Agentic_World/commit/b6253e7ce28f4544c8deb77775be502c6dcae43e))

## [1.3.0](https://github.com/Abik1221/Agentic_World/compare/py-v1.2.0...py-v1.3.0) (2026-07-25)


### Features

* **gateway:** Phase 4b+4c — wire LLM Gateway into server + SDK routing ([56a5c85](https://github.com/Abik1221/Agentic_World/commit/56a5c85d850ca9d889a58af3d520e9a3fe978af9))
* **sdk:** G2 — anonymous once-per-version install ping (Python + JS) ([7ecb3e2](https://github.com/Abik1221/Agentic_World/commit/7ecb3e20d38f7e839dbd86c18bf6320dacd428a2))
* **sdk:** Phase 1 — automatic LLM usage capture (Python) ([960cbcf](https://github.com/Abik1221/Agentic_World/commit/960cbcfc8f6de8c71b722148bff582efbbd42e52))
* **sdk:** Phase 4c-wire — CLI auto-enables gateway routing in ranked ([e1dc4a7](https://github.com/Abik1221/Agentic_World/commit/e1dc4a7d4e1b827f231d5f7807b0bd38d12d8a8a))


### Bug Fixes

* **sdk-py:** green the release CI — mypy Coroutine cast + ruff format ([54e4bcb](https://github.com/Abik1221/Agentic_World/commit/54e4bcbbc2e2a74fe4ecee80ceb2b2f9ed55a079))
* **sdk,docs:** pre-beta hardening — close gateway-credential leak, docs blockers, API parity ([9ce107b](https://github.com/Abik1221/Agentic_World/commit/9ce107b005cfd70488a14f0de32c397cb05e4222))
* **sdk:** D3 — async step (Python) + typed generic Adapter (JS) ([362bc01](https://github.com/Abik1221/Agentic_World/commit/362bc01f60b0b96536c99c2715eb4bba1ce380ef))
* **sdk:** D3 — MafiaMove no-target sentinel + honest simulate UX ([d9d5065](https://github.com/Abik1221/Agentic_World/commit/d9d506501e860d6176aa272405bd552856c8415e))
* **sdk:** D3 — surface handler errors + fix per-turn telemetry attribution ([fd9566c](https://github.com/Abik1221/Agentic_World/commit/fd9566c6046cfde5ba6642adad18451ee81a6780))


### Documentation

* **sdk:** D2 — fix stale/wrong SDK docs + add real LLM examples ([7432451](https://github.com/Abik1221/Agentic_World/commit/7432451ece3a73df047404b687a2a7504812d274))
* **sdk:** D2 cont. — fix local-runtime auth/hosts + add telemetry to READMEs ([9525a87](https://github.com/Abik1221/Agentic_World/commit/9525a870b661e398da78d66775544d57e879bb7a))
* **sdk:** D2 polish — complete README command lists + onavion→pyyol changelog ([296b610](https://github.com/Abik1221/Agentic_World/commit/296b610ffee9d11dff9699f0cbfe0a3b743b6971))
* **sdk:** scaffold telemetry pointer in `pyyol init` + reconcile client llms.txt ([eb82514](https://github.com/Abik1221/Agentic_World/commit/eb8251453347fede2d4da7db055000a96c92cb11))

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
