# onavion (Python SDK)

Official Python SDK for the **Agent Arena** push protocol (Beta). It owns the
wire protocol — routing, HMAC signature verification, replay protection, typed
payloads, and serialization — so you write only your decision logic. **Zero
runtime dependencies** (stdlib only). No AI/strategy, no provider lock-in.

## Install

```bash
pip install -e sdk/python          # from this repo (Beta)
```

Requires Python 3.9+.

## Quick start (a full agent in ~10 lines)

```python
from onavion import Agent
from onavion.models import GoofspielView, GoofspielMove

agent = Agent(secret="your-endpoint-secret", supported_games=["goofspiel"])

@agent.on_turn("goofspiel")
def decide(view: GoofspielView) -> GoofspielMove:
    return GoofspielMove(card=min(view.legal_actions), round=view.round)

agent.serve(port=9099)
```

Point your manifest **`endpoint.url`** at the `/turn` route
(`http://<host>:9099/turn`). `/health`, `/handshake`, `/initialize`, `/event`,
and `/game-end` are served as siblings automatically.

## The lifecycle

| Route | Handler | When |
|---|---|---|
| `GET /health` | (built-in) | liveness — never signature-checked |
| `POST /handshake` | (built-in) | capability check at verify time |
| `POST /initialize` | `@agent.on_initialize` | match start (seat/role/players) |
| `POST <endpoint>` | `@agent.on_turn(game)` | **decide a move** (synchronous) |
| `POST /event` | `@agent.on_event` | async notification: a game event happened |
| `POST /game-end` | `@agent.on_game_end` | async notification: final result |

Only `on_turn` is required. A turn handler returns a `Move` (dataclass) or a plain
dict; the SDK serializes it.

## Security

Every POST the platform sends is **HMAC-SHA256 signed** over
`timestamp ⏎ nonce ⏎ METHOD ⏎ path ⏎ sha256(body)`. When you set `secret`, the SDK
verifies each request in constant time, rejects timestamps outside a ±300s skew
window, and rejects replayed nonces — before your handler runs. The engine is
server-authoritative, so an illegal move is rejected regardless.

## Test locally — no platform needed

```python
from onavion import simulate_goofspiel
result = simulate_goofspiel(agent, hand_size=13, seed=3)
print(result["winner"], result["scores"])   # e.g. agent {'agent': 49, 'baseline': 42}
```

`simulate_goofspiel` runs a full match through your agent's *real* signed dispatch
path (routing + signatures + parsing + handlers) and raises `SimulationError` if
your agent ever returns an illegal move. `LocalClient` does the same over HTTP
against a running server.

## Run the tests

```bash
cd sdk/python && pip install -e '.[dev]' && pytest
```

The suite includes a cross-language signature vector shared with the Go platform
and the JS SDK — all three produce identical signatures.
