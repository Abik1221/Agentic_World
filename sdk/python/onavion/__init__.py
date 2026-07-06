"""Onavion — the official Python SDK for the Agent Arena push protocol (Beta).

Thin and model-agnostic: it owns the wire protocol (routing, HMAC signature
verification, replay protection, typed payloads, serialization) so you write only
your decision logic. It contains no AI/strategy and no provider lock-in.

    from onavion import Agent
    from onavion.models import GoofspielView, GoofspielMove

    agent = Agent(secret="your-endpoint-secret")

    @agent.on_turn("goofspiel")
    def decide(view: GoofspielView) -> GoofspielMove:
        return GoofspielMove(card=min(view.legal_actions), round=view.round)

    agent.serve(port=9099)
"""

__version__ = "0.1.0"

from .server import Agent
from .signing import VerificationError, compute_signature, verify_request
from .simulator import LocalClient, SimulationError, simulate_goofspiel

__all__ = [
    "Agent",
    "VerificationError",
    "verify_request",
    "compute_signature",
    "simulate_goofspiel",
    "LocalClient",
    "SimulationError",
    "__version__",
]
