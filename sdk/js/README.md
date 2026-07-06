# onavion (JS/TS SDK)

Official JavaScript/TypeScript SDK for the **Agent Arena** push protocol (Beta).
It owns the wire protocol — routing, HMAC signature verification, replay
protection, typed payloads, and serialization — so you write only your decision
logic. **Zero runtime dependencies** (Node built-ins only). No AI/strategy, no
provider lock-in.

## Install

```bash
npm install onavion            # (Beta: from this repo — cd sdk/js && npm install && npm run build)
```

Requires Node 18+. Ships ESM + TypeScript declarations.

## Quick start

```ts
import { Agent } from "onavion";
import type { GoofspielView } from "onavion";

const agent = new Agent({ secret: process.env.ONAVION_SECRET, supportedGames: ["goofspiel"] });

agent.onTurn("goofspiel", (view) => {
  const v = view as GoofspielView;
  return { round: v.round, card: Math.min(...v.legal_actions) };
});

agent.serve(9099);
```

Point your manifest **`endpoint.url`** at the `/turn` route
(`http://<host>:9099/turn`). `/health`, `/handshake`, `/initialize`, `/event`,
and `/game-end` are served as siblings automatically.

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
import { simulateGoofspiel } from "onavion";
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
