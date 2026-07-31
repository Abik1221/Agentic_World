"""Regressions from the 1.6.0 pre-beta defect report.

Both of these made an agent look instrumented while producing nothing verifiable —
the worst failure shape, because the developer only finds out when a ranked match is
voided.
"""

import warnings

import pytest

import pyyol
from pyyol._instrument import _detect_provider


def test_instrument_is_callable_more_than_once():
    """`pyyol.instrument()` worked exactly once, then raised
    "'module' object is not callable".

    The function and the submodule share a name, and importing the submodule to
    resolve the function bound the MODULE onto the package. Real attributes beat
    __getattr__, so every later access returned the module. Any long-lived worker or
    test suite calling the documented entrypoint twice crashed.
    """
    for _ in range(3):
        pyyol.instrument()
    assert callable(pyyol.instrument), "pyyol.instrument stopped being callable"


def test_instrument_survives_the_submodule_being_imported_first():
    """The same bug fired on the FIRST call if anything touched the attribute first —
    a submodule import, a type() check, a reloading harness."""
    import importlib

    import pyyol._instrument  # noqa: F401  — the module that used to shadow it

    importlib.reload(pyyol)
    pyyol.instrument()


def test_groq_clients_are_recognised():
    """A native groq client reported no provider, so route() silently no-opped and the
    agent could never be verified. Groq is a mainstream provider."""

    class Client:
        pass

    Client.__module__ = "groq._client"
    assert _detect_provider(Client()) == "groq"


def test_openai_client_pointed_at_groq_still_reports_openai():
    """The common way to use Groq is the OpenAI SDK against their compatible endpoint.
    That IS an OpenAI client and must keep routing as one — the gateway routes by path."""

    class Client:
        pass

    Client.__module__ = "openai._client"
    assert _detect_provider(Client()) == "openai"


def test_route_warns_instead_of_silently_doing_nothing():
    """Silence here is the actual defect: an unrouted client reports zero tokens,
    earns no Verified badge, and in ranked can have matches voided — with nothing
    anywhere telling the developer why."""

    class Client:
        pass

    Client.__module__ = "mystery.client"
    c = Client()

    with pytest.warns(RuntimeWarning, match="NOT routed"):
        out = pyyol.route(c)

    assert out is c, "route() must still return the client for chaining"


def test_route_does_not_warn_for_a_recognised_provider():
    """A warning on the happy path would train people to ignore it."""

    class Client:
        base_url = ""

    Client.__module__ = "openai._client"

    with warnings.catch_warnings():
        warnings.simplefilter("error", RuntimeWarning)
        pyyol.route(Client())  # must not raise
