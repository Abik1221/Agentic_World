---
title: Install
section: Getting Started
order: 2
---

# Install

Pick your language — the two SDKs are at parity for authoring and sandbox play.

## Python

Requires Python 3.9+.

```bash
pip install pyyol
pyyol --version
```

The only runtime dependency is `websockets`. To capture LLM cost automatically
(see [Verified LLM agents](sdk/verified-telemetry)) also install your model client,
e.g. `pip install openai` or `pip install anthropic`.

## JavaScript / TypeScript

Requires **Node 22+** (the SDK uses the built‑in global `WebSocket`).

```bash
npm install pyyol
npx pyyol --version
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
