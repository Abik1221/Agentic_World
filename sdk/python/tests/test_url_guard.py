"""Every outbound request refuses a scheme that is not http(s).

The call sites this replaced carried `# noqa: S310 - our own API`. The URL is not our own API,
it is whatever PYYOL_API / --api / a saved config says it is, so a base of `file:///etc/passwd`
made the client read a local file and parse the bytes as an API response.
"""

from __future__ import annotations

import urllib.request

import pytest

from pyyol._urlguard import UnsafeURLError, guard_url, urlopen


@pytest.mark.parametrize(
    "url",
    [
        "file:///etc/passwd",
        "FILE:///etc/passwd",  # scheme comparison is case-insensitive
        "ftp://example.com/x",  # urllib supports it; nothing here speaks it
        "gopher://example.com/1",
        "/no/scheme/at/all",
        "data:text/plain,hi",
    ],
)
def test_unsafe_schemes_are_refused(url: str) -> None:
    with pytest.raises(UnsafeURLError):
        guard_url(url)


@pytest.mark.parametrize(
    "url",
    ["https://api.pyyol.com/v1/matches", "http://localhost:8080/v1/matches", "HTTPS://X/y"],
)
def test_http_and_https_are_allowed(url: str) -> None:
    assert guard_url(url) == url


def test_urlopen_checks_before_opening(tmp_path) -> None:
    """The guard must fire BEFORE the socket/file is touched.

    Written against a file that really exists: a guard that ran after the open would return its
    contents and the test would pass on a truthy read rather than on the refusal.
    """
    target = tmp_path / "secret.txt"
    target.write_text("token=hunter2")
    with pytest.raises(UnsafeURLError):
        urlopen(f"file://{target}", timeout=5)
    with pytest.raises(UnsafeURLError):
        urlopen(urllib.request.Request(f"file://{target}"), timeout=5)


def test_no_module_calls_urlopen_directly() -> None:
    """Only _urlguard may open a URL.

    This replaces ruff's S310, which fires on every `Request(...)` construction and so had to be
    switched off to stay readable. It checks the property that actually matters: a new call site
    that reaches for urllib directly, skipping the scheme check, fails here — including one added
    to a file that already had a `# noqa` on it.
    """
    import pathlib

    pkg = pathlib.Path(__file__).resolve().parent.parent / "pyyol"
    # Matches RAW urllib use, not any call named urlopen: cli._urlopen is a thin delegate that
    # forwards to _urlguard, and flagging it would train the next reader to widen the exclusion
    # until the test means nothing. `_request` is the module alias telemetry/install_ping use.
    raw = ("urllib.request.urlopen(", "_request.urlopen(", "request.urlopen(")
    offenders = [
        f"{path.name}:{i}"
        for path in sorted(pkg.rglob("*.py"))
        if path.name != "_urlguard.py"
        for i, line in enumerate(path.read_text().splitlines(), 1)
        if any(r in line for r in raw) and not line.lstrip().startswith("#")
    ]
    assert not offenders, (
        "these call urlopen without the scheme guard: "
        + ", ".join(offenders)
        + " — route them through pyyol._urlguard.urlopen"
    )
