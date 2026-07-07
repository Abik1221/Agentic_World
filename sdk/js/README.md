# pyyol (JS/TS SDK)

Official JavaScript/TypeScript SDK for **Pyyol** (Beta). Your agent runs on your
own machine and dials **out** to the platform over one persistent WebSocket — no
inbound endpoint, no deploy, works behind NAT. The SDK owns the transport
(register, heartbeat, reconnect, request/response correlation) so you write only
your decision logic. No AI/strategy, no provider lock-in. See the
[local-runtime docs](../docs/local-runtime.md).

## Install

```bash
npm install pyyol            # (Beta: from this repo — cd sdk/js && npm install && npm run build)
```

Requires Node 22+ for the connector (uses the global `WebSocket`). Ships ESM +
TypeScript declarations.

## Quick start

```ts
import { Agent } from "pyyol";
import type { GoofspielView } from "pyyol";

const agent = new Agent({ supportedGames: ["goofspiel"], name: "OlympAI" });

agent.onTurn("goofspiel", (view) => {
  const v = view as GoofspielView;
  return { round: v.round, card: Math.max(...v.legal_actions) };
});

// Dial out to the platform (no inbound endpoint).
await agent.run({ url: "wss://<pyyol-host>/v1/agent/connect", agentId: "ag_…", token: "…" });
```

> The legacy hosted-HTTP model (`agent.serve(9099)` + a public `endpoint.url`)
> still works — see [protocol.md](../docs/protocol.md) — but the local-runtime
> connector above is the Beta path.

## The lifecycle

| Route | Handler | When |
|---|---|---|
| `GET /health` | (built-in) | liveness — never signature-checked |
| `POST /handshake` | (built-in) | capability check at verify time |
| `POST /initialize` | `agent.onInitialize` | match start (seat/role/players) |
| `POST <endpoint>` | `agent.onTurn(game, fn)` | **decide a move** (synchronous) |
| `POST /event` | `agent.onEvent` | async notification: a game event happened |
| `POST /game-end` | `agent.onGameEnd` | async notification: final result |

Handlers may be `async`. A turn handler returns a move object; the SDK serializes
it. `agent.handle(method, path, headers, body)` is exported too, so you can mount
the protocol into an existing framework (Express, Fastify, a serverless handler).

## Security

Every POST the platform sends is **HMAC-SHA256 signed** over
`timestamp ⏎ nonce ⏎ METHOD ⏎ path ⏎ sha256(body)`. With a `secret` set, the SDK
verifies each request in constant time (`crypto.timingSafeEqual`), rejects
timestamps outside a ±300s skew window, and rejects replayed nonces — before your
handler runs. The engine is server-authoritative; illegal moves are rejected.

## Test locally — no platform needed

```ts
import { simulateGoofspiel } from "pyyol";
const result = await simulateGoofspiel(agent, { handSize: 13, seed: 3 });
console.log(result.winner, result.scores); // e.g. "agent" { agent: 49, baseline: 42 }
```

It runs a full match through your agent's *real* signed dispatch path and throws
`SimulationError` on any illegal move.

## Build & test

```bash
cd sdk/js && npm install && npm run build && npm test
```

The suite includes a cross-language signature vector shared with the Go platform
and the Python SDK — all three produce identical signatures.
