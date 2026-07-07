"""Local simulation harness — test your agent without the platform.

Two ways to exercise an agent:

* :func:`simulate_goofspiel` runs a complete Goofspiel match in-process against a
  baseline opponent, driving your agent through its *real* request path
  (``Agent.handle`` with valid signatures) — so it validates routing, signature
  verification, payload parsing, and your handlers end to end, then reports the
  result. Goofspiel's rules are simple and fully local; it needs no server engine.

* :class:`LocalClient` signs and sends lifecycle requests to a *running* agent
  server over HTTP, exactly as the platform would. Use it to smoke-test a deployed
  endpoint (the ``pyyol`` CLI uses it too).

The simulator contains game *rules* for Goofspiel only (to referee a match); it
contains no strategy — your handler decides every move.
"""

from __future__ import annotations

import json
import random
import time
from typing import Any, Dict, List, Optional

from .models import GOOFSPIEL, PROTOCOL_VERSION
from .signing import (
    REQUEST_ID_HEADER,
    SIGNATURE_HEADER,
    SIGNATURE_VERSION,
    TIMESTAMP_HEADER,
    compute_signature,
)


def _signed_headers(
    secret: str, method: str, path: str, body: bytes, nonce: str, ts: str
) -> Dict[str, str]:
    """Build the headers the platform sends, with a valid signature when a secret
    is configured (matching agentclient)."""
    headers = {
        TIMESTAMP_HEADER: ts,
        REQUEST_ID_HEADER: nonce,
        "Content-Type": "application/json",
    }
    if secret:
        sig = compute_signature(secret, ts, nonce, method, path, body)
        headers[SIGNATURE_HEADER] = f"{SIGNATURE_VERSION}={sig}"
        headers["Authorization"] = "Bearer " + secret
    return headers


def _post(agent, secret: str, path: str, payload: Dict[str, Any], seq: int) -> Dict[str, Any]:
    """POST a JSON body through the agent's real dispatch with a valid signature."""
    body = json.dumps(payload).encode()
    nonce = f"sim_{seq}"
    ts = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    headers = _signed_headers(secret, "POST", path, body, nonce, ts)
    status, resp = agent.handle("POST", path, headers, body)
    if status != 200:
        raise SimulationError(f"agent returned {status} for {path}: {resp}")
    return resp


class SimulationError(Exception):
    pass


def simulate_goofspiel(
    agent,
    *,
    hand_size: int = 13,
    seed: int = 1,
    shuffle_prizes: bool = False,
    turn_path: str = "/turn",
) -> Dict[str, Any]:
    """Play one Goofspiel match: your agent (seat 0) vs a baseline (seat 1).

    Returns a result dict: ``{winner, scores, rounds, moves}``. Raises
    :class:`SimulationError` if your agent returns an illegal or malformed move,
    so a broken decision loop fails loudly in local testing.
    """
    secret = agent.secret
    rng = random.Random(seed)

    prizes = list(range(1, hand_size + 1))
    if shuffle_prizes:
        rng.shuffle(prizes)

    dev_hand = list(range(1, hand_size + 1))
    opp_hand = list(range(1, hand_size + 1))
    scores = [0, 0]
    carried = 0
    moves: List[Dict[str, Any]] = []
    seq = 0

    # Lifecycle: initialize.
    _post(
        agent,
        secret,
        "/initialize",
        {
            "protocol": PROTOCOL_VERSION,
            "match_id": "sim-goofspiel",
            "game": GOOFSPIEL,
            "seat": 0,
            "players": 2,
        },
        seq,
    )
    seq += 1

    for rnd, prize in enumerate(prizes):
        pool = prize + carried
        view = {
            "game": GOOFSPIEL,
            "match_id": "sim-goofspiel",
            "seat": 0,
            "round": rnd,
            "current_prize": prize,
            "prize_pool": pool,
            "your_hand": list(dev_hand),
            "scores": list(scores),
            "legal_actions": list(dev_hand),
        }
        resp = _post(agent, secret, turn_path, view, seq)
        seq += 1

        dev_card = resp.get("card")
        if dev_card not in dev_hand:
            raise SimulationError(f"round {rnd}: agent bid {dev_card!r}, not in hand {dev_hand}")
        opp_card = _baseline_bid(opp_hand, pool)

        dev_hand.remove(dev_card)
        opp_hand.remove(opp_card)
        moves.append({"round": rnd, "prize": prize, "pool": pool, "dev": dev_card, "opp": opp_card})

        if dev_card > opp_card:
            scores[0] += pool
            carried = 0
        elif opp_card > dev_card:
            scores[1] += pool
            carried = 0
        else:
            carried = pool  # tie — the pool carries into the next round

        # Lifecycle: async event that the round resolved.
        _post(
            agent,
            secret,
            "/event",
            {
                "protocol": PROTOCOL_VERSION,
                "match_id": "sim-goofspiel",
                "game": GOOFSPIEL,
                "seq": rnd,
                "type": "round_revealed",
                "payload": {"prize": prize, "dev_card": dev_card, "opp_card": opp_card},
            },
            seq,
        )
        seq += 1

    winner = 0 if scores[0] > scores[1] else 1 if scores[1] > scores[0] else -1

    # Lifecycle: game-end.
    _post(
        agent,
        secret,
        "/game-end",
        {
            "protocol": PROTOCOL_VERSION,
            "match_id": "sim-goofspiel",
            "game": GOOFSPIEL,
            "result": {"winner_seat": winner, "scores": scores},
        },
        seq,
    )

    return {
        "winner": "agent" if winner == 0 else "baseline" if winner == 1 else "tie",
        "winner_seat": winner,
        "scores": {"agent": scores[0], "baseline": scores[1]},
        "rounds": len(prizes),
        "moves": moves,
    }


def _baseline_bid(hand: List[int], pool: int) -> int:
    """Deterministic opponent: bid the card nearest the pool value (ties -> lower)."""
    return min(hand, key=lambda c: (abs(c - pool), c))


class LocalClient:
    """Signs + sends lifecycle requests to a running agent server over HTTP,
    exactly as the platform would. Zero dependencies (urllib)."""

    def __init__(self, base_url: str, secret: str = ""):
        self.base_url = base_url.rstrip("/")
        self.secret = secret

    def health(self) -> Dict[str, Any]:
        return self._request("GET", "/health", None)

    def handshake(self) -> Dict[str, Any]:
        return self._request(
            "POST", "/handshake", {"platform": "agent-arena", "protocol": PROTOCOL_VERSION}
        )

    def turn(self, view: Dict[str, Any], turn_path: str = "/turn") -> Dict[str, Any]:
        return self._request("POST", turn_path, view)

    def initialize(self, body: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/initialize", body)

    def _request(self, method: str, path: str, payload: Optional[Dict[str, Any]]) -> Dict[str, Any]:
        import urllib.request

        body = json.dumps(payload).encode() if payload is not None else b""
        nonce = f"cli_{int(time.time() * 1000)}"
        ts = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        headers = (
            _signed_headers(self.secret, method, path, body, nonce, ts)
            if payload is not None
            else {}
        )
        req = urllib.request.Request(
            self.base_url + path, data=body or None, method=method, headers=headers
        )
        with urllib.request.urlopen(req, timeout=10) as resp:
            raw = resp.read()
        return json.loads(raw) if raw else {}
