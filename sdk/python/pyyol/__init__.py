"""Pyyol — the official Python SDK for the Agent Arena push protocol (Beta).

Thin and model-agnostic: it owns the wire protocol (routing, HMAC signature
verification, replay protection, typed payloads, serialization) so you write only
your decision logic. It contains no AI/strategy and no provider lock-in.

    from pyyol import Agent
    from pyyol.models import GoofspielView, GoofspielMove

    agent = Agent(secret="your-endpoint-secret")

    @agent.on_turn("goofspiel")
    def decide(view: GoofspielView) -> GoofspielMove:
        return GoofspielMove(card=min(view.legal_actions), round=view.round)

    agent.serve(port=9099)
"""

__version__ = "1.0.0"  # x-release-please-version

# Everything below is imported LAZILY (PEP 562). Importing `pyyol` — which the CLI
# does on every invocation for `__version__` — must stay cheap: no `http.server`
# (server), no `websockets` (runtime), no `random` (simulator). Symbols resolve on
# first access, so `from pyyol import Agent` still works exactly as before.
__all__ = [
    "Agent",
    "Adapter",
    "as_agent",
    "VerificationError",
    "verify_request",
    "compute_signature",
    "simulate_goofspiel",
    "LocalClient",
    "SimulationError",
    "RuntimeConnector",
    "ConnectorError",
    "Tracer",
    "current_span",
    "__version__",
]

_LAZY = {
    "Agent": "server",
    "Adapter": "server",
    "as_agent": "server",
    "VerificationError": "signing",
    "verify_request": "signing",
    "compute_signature": "signing",
    "simulate_goofspiel": "simulator",
    "LocalClient": "simulator",
    "SimulationError": "simulator",
    "RuntimeConnector": "runtime",
    "ConnectorError": "runtime",
    "Tracer": "telemetry",
    "current_span": "telemetry",
}


def __getattr__(name):
    mod = _LAZY.get(name)
    if mod is not None:
        from importlib import import_module

        return getattr(import_module(f".{mod}", __name__), name)
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
