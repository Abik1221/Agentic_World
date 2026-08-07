"""HMAC request signing + verification for the Pyyol push protocol.

Every request the platform sends to a developer endpoint is signed with
HMAC-SHA256 over a canonical string that binds the timestamp, a per-request
nonce, the HTTP method, the request path, and a hash of the body. An agent
verifies the signature (constant-time), rejects stale timestamps (clock-skew
window), and rejects already-seen nonces (replay protection).

This reproduces EXACTLY the platform's ``agentclient.SignRequest`` (Go):

    signingString = timestamp \\n nonce \\n METHOD \\n path \\n hex(sha256(body))
    signature     = hex(hmacSHA256(secret, signingString))

The shared secret is the agent's endpoint token (the manifest endpoint secret).
"""

from __future__ import annotations

import hashlib
import hmac
import threading
import time
from collections.abc import Mapping

SIGNATURE_VERSION = "v1"
SIGNATURE_HEADER = "X-Arena-Signature"
TIMESTAMP_HEADER = "X-Arena-Timestamp"
REQUEST_ID_HEADER = "X-Arena-Request-Id"

# Default clock-skew tolerance for the timestamp, in seconds. A request whose
# timestamp is more than this far from local time (either direction) is rejected.
DEFAULT_SKEW_SECONDS = 300


class VerificationError(Exception):
    """Raised when a request signature fails verification.

    ``reason`` is a short machine-readable code (missing_signature,
    unsupported_version, stale_timestamp, replayed_nonce, bad_signature) suitable
    for logging or returning as an error body.
    """

    def __init__(self, reason: str, message: str = ""):
        self.reason = reason
        super().__init__(message or reason)


def canonical_string(timestamp: str, nonce: str, method: str, path: str, body: bytes) -> str:
    """Build the canonical signing string. Matches the Go builder exactly."""
    body_hash = hashlib.sha256(body or b"").hexdigest()
    return "\n".join([timestamp, nonce, method.upper(), path, body_hash])


def compute_signature(
    secret: str, timestamp: str, nonce: str, method: str, path: str, body: bytes
) -> str:
    """Return the hex HMAC-SHA256 signature for a request."""
    signing = canonical_string(timestamp, nonce, method, path, body).encode()
    return hmac.new(secret.encode(), signing, hashlib.sha256).hexdigest()


class ReplayGuard:
    """Bounded in-memory nonce cache that rejects replayed request ids.

    Nonces are held for ``ttl`` seconds (>= the skew window, so anything still
    within the accepted timestamp range is remembered). The cache is size-capped
    so a flood of unique nonces cannot exhaust memory; when full, the oldest
    entries are evicted. Thread-safe for use behind a threaded HTTP server.
    """

    def __init__(self, ttl: float = DEFAULT_SKEW_SECONDS * 2, max_size: int = 50_000):
        self._ttl = ttl
        self._max = max_size
        self._seen: dict[str, float] = {}
        self._lock = threading.Lock()

    def check_and_store(self, nonce: str, now: float) -> bool:
        """Return True if ``nonce`` is fresh (and remember it); False if replayed."""
        with self._lock:
            self._evict(now)
            if nonce in self._seen:
                return False
            if len(self._seen) >= self._max:
                # Drop the oldest entry to stay bounded.
                oldest = min(self._seen, key=lambda k: self._seen[k])
                del self._seen[oldest]
            self._seen[nonce] = now
            return True

    def _evict(self, now: float) -> None:
        cutoff = now - self._ttl
        stale = [k for k, t in self._seen.items() if t < cutoff]
        for k in stale:
            del self._seen[k]


def _header(headers: Mapping[str, str], name: str) -> str:
    """Case-insensitive header lookup that works for dicts and http.client
    Message objects (which are already case-insensitive)."""
    if hasattr(headers, "get"):
        val = headers.get(name)
        if val is not None:
            return val
    lower = name.lower()
    for k, v in dict(headers).items():
        if k.lower() == lower:
            return v
    return ""


def verify_request(
    secret: str,
    headers: Mapping[str, str],
    method: str,
    path: str,
    body: bytes,
    *,
    skew_seconds: int = DEFAULT_SKEW_SECONDS,
    replay_guard: ReplayGuard | None = None,
    now: float | None = None,
) -> None:
    """Verify a signed request or raise :class:`VerificationError`.

    ``path`` must be the path the platform signed — the request path as received
    (no query string). ``body`` is the exact raw request body bytes.
    """
    sig_header = _header(headers, SIGNATURE_HEADER)
    ts = _header(headers, TIMESTAMP_HEADER)
    nonce = _header(headers, REQUEST_ID_HEADER)

    if not sig_header or not ts or not nonce:
        raise VerificationError("missing_signature", "missing signature/timestamp/nonce headers")

    version, _, provided = sig_header.partition("=")
    if version != SIGNATURE_VERSION or not provided:
        raise VerificationError("unsupported_version", f"unsupported signature version {version!r}")

    now = time.time() if now is None else now
    ts_epoch = _parse_rfc3339(ts)
    if ts_epoch is None or abs(now - ts_epoch) > skew_seconds:
        raise VerificationError("stale_timestamp", "timestamp outside the allowed skew window")

    if replay_guard is not None and not replay_guard.check_and_store(nonce, now):
        raise VerificationError("replayed_nonce", "request id was already used")

    expected = compute_signature(secret, ts, nonce, method, path, body)
    if not hmac.compare_digest(expected, provided):
        raise VerificationError("bad_signature", "signature mismatch")


def _parse_rfc3339(value: str) -> float | None:
    """Parse an RFC3339/ISO-8601 UTC timestamp to epoch seconds (best-effort)."""
    v = value.strip()
    if v.endswith("Z"):
        v = v[:-1] + "+00:00"
    try:
        from datetime import datetime

        return datetime.fromisoformat(v).timestamp()
    except ValueError:
        return None
