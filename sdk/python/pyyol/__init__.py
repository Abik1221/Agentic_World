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

__version__ = "1.2.0"  # x-release-please-version

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
    "current_usage",
    "instrument",
    "uninstrument",
    "record_response",
    "route",
    "enable_gateway",
    "disable_gateway",
    "gateway_base_url",
    "gateway_headers",
    "extract_usage",
    # Cost/pricing helpers (parity with the JS SDK).
    "estimate_cost",
    "rate_for",
    "is_known",
    "PRICING_VERSION",
    # Telemetry + signing primitives (parity with the JS SDK).
    "Span",
    "UsageAccumulator",
    "match_trace_id",
    "ReplayGuard",
    "canonical_string",
    "SIGNATURE_VERSION",
    # Typed game models — re-exported here so `from pyyol import GoofspielView`
    # works (parity with the JS SDK's `export * from "./models"`).
    "GoofspielView",
    "GoofspielMove",
    "MonopolyView",
    "MonopolyMove",
    "MafiaView",
    "MafiaMove",
    "parse_view",
    "move_to_dict",
    "game_rules",
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
    "current_usage": "telemetry",
    "instrument": "instrument",
    "uninstrument": "instrument",
    "record_response": "instrument",
    "route": "instrument",
    "enable_gateway": "instrument",
    "disable_gateway": "instrument",
    "gateway_base_url": "instrument",
    "gateway_headers": "instrument",
    "extract_usage": "instrument",
    "estimate_cost": "pricing",
    "rate_for": "pricing",
    "is_known": "pricing",
    "PRICING_VERSION": "pricing",
    "Span": "telemetry",
    "UsageAccumulator": "telemetry",
    "match_trace_id": "telemetry",
    "ReplayGuard": "signing",
    "canonical_string": "signing",
    "SIGNATURE_VERSION": "signing",
    # models is annotation-only + stdlib, so importing it stays cheap.
    "GoofspielView": "models",
    "GoofspielMove": "models",
    "MonopolyView": "models",
    "MonopolyMove": "models",
    "MafiaView": "models",
    "MafiaMove": "models",
    "parse_view": "models",
    "move_to_dict": "models",
}


def game_rules(game: str = "") -> str:
    """The engine-generated rules bundled with this package (all 3 games as
    Markdown). Pass a game name ("goofspiel"|"monopoly"|"mafia") to slice just that
    section, or nothing for the full reference. This is the same text an LLM/agent
    author needs — it ships INSIDE the wheel (pyyol/rules/games.md), so it is always
    available offline and can never drift from the deployed engine."""
    from importlib import resources

    text = (resources.files(__name__) / "rules" / "games.md").read_text(encoding="utf-8")
    if not game:
        return text
    # Section headers in games.md are "## <Game>" — return that section only.
    marker = f"## {game.capitalize()}"
    start = text.find(marker)
    if start == -1:
        return text
    nxt = text.find("\n## ", start + len(marker))
    return text[start:] if nxt == -1 else text[start:nxt]


def __getattr__(name):
    mod = _LAZY.get(name)
    if mod is not None:
        from importlib import import_module

        return getattr(import_module(f".{mod}", __name__), name)
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
