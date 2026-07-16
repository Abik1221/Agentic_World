"""Local credential storage for the ``pyyol`` CLI.

Credentials from ``pyyol login`` are stored securely: the access/refresh
tokens go into the OS secret store via the optional ``keyring`` package
(Keychain on macOS, Credential Manager on Windows, Secret Service on Linux) when
it is installed; otherwise they fall back to a ``0600`` file under the config
directory. Non-secret metadata (platform URL, agent id) is always kept in that
file so the CLI can show which platform you're logged into without unlocking the
secret store.

Never asks the developer to paste an API key during normal onboarding — the token
arrives via the browser login flow and is written here.
"""

from __future__ import annotations

import json
import os
import stat
from dataclasses import asdict, dataclass
from typing import Optional

SERVICE = "pyyol"
_KEYRING_KEY = "access_token"


def config_dir() -> str:
    """The pyyol config directory. Override with ``PYYOL_HOME`` (tests, CI)."""
    base = os.environ.get("PYYOL_HOME")
    if base:
        return base
    return os.path.join(os.path.expanduser("~"), ".pyyol")


def _cred_path() -> str:
    return os.path.join(config_dir(), "credentials.json")


@dataclass
class Credentials:
    url: str = ""  # platform API/base URL
    connect_url: str = ""  # WSS connect URL (derived if empty)
    agent_id: str = ""
    access_token: str = ""
    refresh_token: str = ""


def _try_keyring():
    try:
        import keyring  # type: ignore

        return keyring
    except Exception:  # noqa: BLE001 — any import/backend error → file fallback
        return None


def save(creds: Credentials) -> str:
    """Persist credentials. Returns the storage backend used ("keyring"|"file")."""
    d = config_dir()
    os.makedirs(d, exist_ok=True)
    try:
        os.chmod(d, 0o700)
    except OSError:
        pass

    backend = "file"
    meta = asdict(creds)
    kr = _try_keyring()
    if kr is not None and creds.access_token:
        try:
            kr.set_password(SERVICE, _KEYRING_KEY, creds.access_token)
            if creds.refresh_token:
                kr.set_password(SERVICE, "refresh_token", creds.refresh_token)
            # Don't duplicate secrets into the file when the keyring holds them.
            meta["access_token"] = ""
            meta["refresh_token"] = ""
            backend = "keyring"
        except Exception:  # noqa: BLE001 — backend locked/unavailable → file fallback
            backend = "file"

    path = _cred_path()
    # Create with 0600 from the start (O_CREAT + mode) so the token is never written
    # to a world/group-readable file, even briefly. os.open honors the mode only on
    # creation, so chmod an existing file too (re-login keeps it locked down).
    flags = os.O_WRONLY | os.O_CREAT | os.O_TRUNC
    fd = os.open(path, flags, stat.S_IRUSR | stat.S_IWUSR)  # 0600
    with os.fdopen(fd, "w") as f:
        json.dump(meta, f, indent=2)
    try:
        os.chmod(path, stat.S_IRUSR | stat.S_IWUSR)
    except OSError:
        pass
    return backend


def load() -> Optional[Credentials]:
    """Load stored credentials, or None if not logged in."""
    path = _cred_path()
    if not os.path.exists(path):
        return None
    try:
        with open(path) as f:
            data = json.load(f)
    except (OSError, json.JSONDecodeError):
        return None
    creds = Credentials(
        url=data.get("url", ""),
        connect_url=data.get("connect_url", ""),
        agent_id=data.get("agent_id", ""),
        access_token=data.get("access_token", ""),
        refresh_token=data.get("refresh_token", ""),
    )
    if not creds.access_token:
        kr = _try_keyring()
        if kr is not None:
            try:
                creds.access_token = kr.get_password(SERVICE, _KEYRING_KEY) or ""
                creds.refresh_token = kr.get_password(SERVICE, "refresh_token") or ""
            except Exception:  # noqa: BLE001
                pass
    return creds


def clear() -> bool:
    """Remove stored credentials. Returns True if anything was removed."""
    removed = False
    kr = _try_keyring()
    if kr is not None:
        for key in (_KEYRING_KEY, "refresh_token"):
            try:
                kr.delete_password(SERVICE, key)
                removed = True
            except Exception:  # noqa: BLE001 — not present / locked
                pass
    path = _cred_path()
    if os.path.exists(path):
        try:
            os.remove(path)
            removed = True
        except OSError:
            pass
    return removed
