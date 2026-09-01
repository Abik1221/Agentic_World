"""Anonymous, once-per-version SDK install ping.

The first time the CLI runs a given SDK version, we fire a single best-effort ping so
the platform can show adoption analytics (downloads over time + per-country). It is:

  * anonymous — we send only ``{sdk, version}``; the server resolves a COUNTRY from
    the request (CF-IPCountry) and never stores the IP;
  * fire-and-forget — a daemon thread with a short timeout, so it never blocks or
    errors the CLI;
  * once per version — a marker file in the config dir dedups it;
  * opt-out — set ``PYYOL_NO_TELEMETRY=1`` or the standard ``DO_NOT_TRACK=1``.
"""

from __future__ import annotations

from . import _urlguard

import json
import os
import threading
from urllib import request as _request

from .credentials import config_dir

_TRUTHY_OFF = {"", "0", "false", "no", "off"}


def _opted_out() -> bool:
    for key in ("PYYOL_NO_TELEMETRY", "DO_NOT_TRACK"):
        v = os.environ.get(key, "").strip().lower()
        if v and v not in _TRUTHY_OFF:
            return True
    return False


def maybe_ping(api_base: str, version: str) -> None:
    """Fire the install ping at most once per version. Never raises."""
    if not api_base or _opted_out():
        return
    marker = os.path.join(config_dir(), f".install_pinged_{version}")
    try:
        if os.path.exists(marker):
            return
        # Mark BEFORE firing so we attempt at most once per version (no retry storm if
        # the endpoint is down); adoption analytics tolerate the rare missed first run.
        os.makedirs(config_dir(), exist_ok=True)
        # No content, so the encoding is academic — named anyway so the "every text open()
        # declares its encoding" rule has no exceptions to argue about.
        with open(marker, "w", encoding="utf-8"):
            pass
    except OSError:
        return

    def _fire() -> None:
        try:
            body = json.dumps({"sdk": "python", "version": version}).encode("utf-8")
            req = _request.Request(
                api_base.rstrip("/") + "/v1/telemetry/install",
                data=body,
                method="POST",
                headers={"Content-Type": "application/json"},
            )
            with _urlguard.urlopen(req, timeout=3) as resp:
                resp.read()
        except Exception:  # noqa: BLE001 - telemetry must never surface to the CLI
            pass

    threading.Thread(target=_fire, name="pyyol-install-ping", daemon=True).start()
