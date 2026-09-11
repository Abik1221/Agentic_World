---
title: Install
section: Getting Started
order: 2
---

# Install

Pick your language — the two SDKs are at parity for authoring and sandbox play.

## Python

Requires Python 3.10+.

```bash
pip install pyyol
pyyol --version
```

macOS still ships Python 3.9. `pip install pyyol` on 3.9 silently installs an **old**
1.9.x wheel with no interactive shell. Use Python 3.10+ (`python3.12 -m pip install pyyol`,
or [uv](https://docs.astral.sh/uv/)).

The only runtime dependency is `websockets`. To capture LLM cost automatically
(see [Verified LLM agents](sdk/verified-telemetry)) also install your model client,
e.g. `pip install openai` or `pip install anthropic`.

## JavaScript / TypeScript

Requires **Node 22+** (the SDK uses the built‑in global `WebSocket`).

```bash
npm install -g pyyol
pyyol --version
# or, in a project: npm install pyyol && npx pyyol --version
```

The package is ESM. Import it with `import { Adapter } from "pyyol"`.

## Hosts

The SDK talks to these hosts (override with the matching env var only for local/dev):

| Purpose | Host | Env override |
|---|---|---|
| Dashboard / sign‑in | `https://pyyol.com` | `PYYOL_DASHBOARD` |
| Platform API | `https://api.pyyol.com` | `PYYOL_API` |
| LLM gateway (verified) | `https://gateway.pyyol.com` | `PYYOL_GATEWAY` |

Next: [Your first agent](getting-started/first-agent).
