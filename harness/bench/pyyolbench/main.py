"""Entrypoint: build one benchmark agent from the environment and serve it.

Configured entirely by environment variables so a container needs no arguments and a
compose file is the whole run definition:

    PYYOLBENCH_MODEL      short key or OpenRouter slug        (required)
    PYYOLBENCH_GAME       goofspiel | mafia | monopoly        (required)
    PYYOLBENCH_SEAT       label used in names and filenames   (default "0")
    PYYOLBENCH_PORT       HTTP port to serve on               (default 9101)
    PYYOLBENCH_HOST       bind address                        (default 0.0.0.0)
    PYYOLBENCH_STATE      shared state dir (budget + ledgers) (default ./state)
    PYYOLBENCH_BUDGET_USD hard ceiling for the WHOLE run      (default 10.00)
    PYYOLBENCH_RUN        run label, groups ledger files      (default "run")

    OPENROUTER_API_KEY    the one key all five models share   (required)
    PYYOL_AGENT_KEY       agent-scope key from onboarding     (required for verified play)
    PYYOL_GATEWAY_BASE    e.g. http://pyyol-backend:8080/v1   (required for verified play)
    PYYOL_SECRET          request-signing secret from onboarding

# Why the gateway settings are "required for verified play" rather than just required

The agent runs perfectly well without them — it plays the game, the moves are legal, the
match completes. What it does not do is earn a turn proof, and without a proof the decision
cannot be completion-bound, so the seat is excluded from the model board. That is a run that
costs full price and publishes nothing. The startup log therefore says loudly which mode it
is in, because the difference is invisible until the board comes up empty.
"""

from __future__ import annotations

import logging
import os
import sys
from pathlib import Path

import pyyol
from pyyol import enable_gateway

from .agent import BenchAgent
from .brain import Brain
from .budget import Budget
from .games.goofspiel import GoofspielPolicy
from .games.mafia import MafiaPolicy
from .games.monopoly import MonopolyPolicy
from .ledger import Ledger
from .models import resolve

POLICIES = {
    "goofspiel": GoofspielPolicy,
    "mafia": MafiaPolicy,
    "monopoly": MonopolyPolicy,
}


def _env(name: str, default: str = "") -> str:
    return (os.environ.get(name) or default).strip()


def build() -> tuple[BenchAgent, str, int]:
    logging.basicConfig(
        level=logging.INFO, format="%(asctime)s %(levelname)-5s %(name)s | %(message)s"
    )
    log = logging.getLogger("pyyolbench")

    model_name = _env("PYYOLBENCH_MODEL")
    game = _env("PYYOLBENCH_GAME")
    if not model_name or game not in POLICIES:
        raise SystemExit(
            "PYYOLBENCH_MODEL and PYYOLBENCH_GAME are required; "
            f"game must be one of {sorted(POLICIES)}"
        )
    model = resolve(model_name)
    policy = POLICIES[game]()

    seat = _env("PYYOLBENCH_SEAT", "0")
    run = _env("PYYOLBENCH_RUN", "run")
    state_dir = Path(_env("PYYOLBENCH_STATE", "./state"))
    budget = Budget(state_dir / "budget.json", float(_env("PYYOLBENCH_BUDGET_USD", "10.00")))
    ledger = Ledger(state_dir / "ledger" / run / f"{model.key}-{game}-seat{seat}.jsonl")

    api_key = _env("OPENROUTER_API_KEY")
    if not api_key:
        raise SystemExit("OPENROUTER_API_KEY is required")

    # Fingerprint the scaffold and capture usage on every call. Without this the SDK
    # observes nothing, so the seat has no scaffold id and cannot be paired with the same
    # harness on another model — which is the entire point of the run.
    pyyol.instrument()

    agent_key = _env("PYYOL_AGENT_KEY")
    gw_base = _env("PYYOL_GATEWAY_BASE")
    verified = bool(agent_key and gw_base)
    if verified:
        enable_gateway(agent_key, gw_base)
        log.info("gateway routing ENABLED via %s — decisions will be completion-bound", gw_base)
    else:
        log.warning(
            "gateway routing DISABLED (PYYOL_AGENT_KEY / PYYOL_GATEWAY_BASE unset). "
            "This agent will play, but its decisions carry no turn proof, cannot be "
            "completion-bound, and will be EXCLUDED from the model board. "
            "That is a full-price run that publishes nothing — set both before spending."
        )

    # Imported here rather than at module scope so a missing `openai` package fails with a
    # clear message at startup instead of at import time in an unrelated test.
    from openai import OpenAI

    raw_client = OpenAI(api_key=api_key, base_url="https://openrouter.ai/api/v1")
    # `provider=` is explicit and load-bearing: route() would otherwise sniff the client
    # class, see an OpenAI SDK object and send the traffic down the gateway's `openai`
    # upstream — a real endpoint, the wrong one, and the failure would be a 401 from
    # OpenAI rather than anything naming OpenRouter.
    client = pyyol.route(raw_client, provider="openrouter")

    brain = Brain(
        model=model,
        policy=policy,
        client=client,
        budget=budget,
        ledger=ledger,
        seat_label=seat,
    )
    agent = BenchAgent(
        brain=brain,
        # The name carries the model, which is what makes a container's logs and the
        # platform's agent list line up without a lookup table.
        name=f"bench-{model.key}-{game}-s{seat}",
        secret=_env("PYYOL_SECRET"),
    )

    snap = budget.snapshot()
    log.info(
        "model=%s game=%s seat=%s | budget $%.2f limit, $%.4f already committed, $%.4f left",
        model.slug, game, seat, snap.limit_usd, snap.committed_usd, snap.remaining_usd,
    )
    return agent, _env("PYYOLBENCH_HOST", "0.0.0.0"), int(_env("PYYOLBENCH_PORT", "9101"))


def main() -> int:
    agent, host, port = build()
    agent.to_agent().serve(host=host, port=port)
    return 0


if __name__ == "__main__":
    sys.exit(main())
