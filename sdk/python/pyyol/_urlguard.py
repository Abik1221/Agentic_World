"""Scheme guard for every outbound request the SDK makes.

``urlopen`` honours whatever scheme it is handed. Every base URL in this SDK comes from
somewhere the user controls — a ``--api`` flag, ``PYYOL_API``, a saved config file, a
``PYYOL_TELEMETRY_URL`` — so a base of ``file:///etc/passwd`` makes the client read a local
file and parse the bytes as an API response, and a custom scheme reaches whatever opener is
registered for it.

The call sites used to carry ``# noqa: S310 - our own API``. That reasoning was optimistic: the
URL is not our own API, it is whatever the environment says our API is. The check is cheap and
the assumption was the only thing standing in for it.

Guarding at the point of the OPEN rather than where the base is parsed is deliberate. URLs are
assembled in a dozen places across the CLI, the runtime and the telemetry client; a check that
sits next to one parse is one refactor away from being bypassed, and the next call site added
would not inherit it. This one cannot be walked around, because nothing else opens a socket.
"""

from __future__ import annotations

import urllib.parse
import urllib.request

# http and https only. Notably excludes ftp, which urllib still supports and which no part of
# this SDK has any reason to speak.
ALLOWED_URL_SCHEMES = frozenset({"http", "https"})


class UnsafeURLError(ValueError):
    """Raised when a URL's scheme is not one this SDK will open.

    A distinct type so a caller can tell a rejected scheme from a network failure. The CLI
    turns it into a message about which setting to fix; the agent runtime lets it propagate,
    because an agent pointed at a ``file://`` base is misconfigured in a way that retrying
    cannot resolve.
    """


def guard_url(url: str) -> str:
    """Return ``url`` unchanged, or raise :class:`UnsafeURLError`."""
    scheme = urllib.parse.urlsplit(url).scheme.lower()
    if scheme not in ALLOWED_URL_SCHEMES:
        raise UnsafeURLError(
            f"refusing to open {scheme or 'scheme-less'} URL: {url!r} — "
            "the API base must be http:// or https://"
        )
    return url


def urlopen(req, timeout: float):
    """``urllib.request.urlopen`` with the scheme checked first.

    Accepts a ``Request`` or a bare URL string, matching urlopen itself, so call sites convert
    by changing only the name they call.
    """
    guard_url(req.full_url if isinstance(req, urllib.request.Request) else req)
    return urllib.request.urlopen(req, timeout=timeout)  # noqa: S310 - scheme checked above
