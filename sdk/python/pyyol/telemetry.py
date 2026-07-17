"""Optional, opt-in agent telemetry for Pyyol Lens.

The SDK ships per-turn spans (and any model/tool calls the developer records) to
the Pyyol Lens ingest, correlated to the SAME match trace the platform emits: the
trace id is ``match_<match_id>`` on both sides, so a match's authoritative
gateway spans and the agent's own model/tool spans render in one waterfall.

Design (mirrors the Go emitter):
  * Zero dependencies — urllib + a daemon thread + a bounded queue.
  * Non-blocking: a full queue drops (and counts); telemetry never slows a turn.
  * Disabled by default: enabled only when ``PYYOL_LENS_ENDPOINT`` and
    ``PYYOL_LENS_API_KEY`` are set. A disabled tracer is a cheap no-op.

Manual API for agent authors, inside an ``on_turn`` handler::

    from pyyol import current_span
    span = current_span()
    span.log_model_call(provider="openai", model="gpt-4o",
                        prompt_tokens=1200, completion_tokens=80, latency_ms=740)
    span.log("picked high card", reason="opponent low on aces")
"""

from __future__ import annotations

import contextvars
import json
import os
import queue
import threading
import time
import uuid
from typing import Any, Dict, List, Optional
from urllib import request as _request

SCHEMA_VERSION = "2026-04-17"

# The active span, so handler code can reach it via current_span().
_current: "contextvars.ContextVar[Optional[Span]]" = contextvars.ContextVar(
    "pyyol_current_span", default=None
)


def match_trace_id(match_id: str) -> str:
    """Stable trace id shared with the platform engine (Go: MatchTraceID)."""
    return f"match_{match_id}" if match_id else ""


class Span:
    """A live span. Records child model/tool calls and free-form logs. All methods
    are safe no-ops when telemetry is disabled."""

    def __init__(self, tracer: "Tracer", trace_id: str, span_id: str, base: Dict[str, Any]):
        self._t = tracer
        self.trace_id = trace_id
        self.span_id = span_id
        self._base = base

    def log_model_call(
        self,
        provider: str = "",
        model: str = "",
        prompt_tokens: int = 0,
        completion_tokens: int = 0,
        total_tokens: int = 0,
        estimated_cost: float = 0.0,
        latency_ms: int = 0,
        **payload: Any,
    ) -> None:
        self._t._emit(
            {
                **self._base,
                "event_type": "model_call_completed",
                "parent_span_id": self.span_id,
                "span_id": _id(),
                "span_type": "model_call",
                "status": "ok",
                "provider": provider,
                "model": model,
                "prompt_tokens": prompt_tokens,
                "completion_tokens": completion_tokens,
                "total_tokens": total_tokens or (prompt_tokens + completion_tokens),
                "estimated_cost": estimated_cost,
                "latency_ms": latency_ms,
                "payload_json": payload or None,
            }
        )

    def log_tool_call(
        self, name: str, latency_ms: int = 0, status: str = "ok", **payload: Any
    ) -> None:
        failed = status not in ("ok", "success", "")
        self._t._emit(
            {
                **self._base,
                "event_type": "tool_call_failed" if failed else "tool_call_completed",
                "parent_span_id": self.span_id,
                "span_id": _id(),
                "span_type": "tool_call",
                "status": "error" if failed else "ok",
                "tool_name": name,
                "latency_ms": latency_ms,
                "payload_json": payload or None,
            }
        )

    def log(self, message: str, level: str = "info", **fields: Any) -> None:
        self._t._emit(
            {
                **self._base,
                "event_type": "log_record",
                "parent_span_id": self.span_id,
                "span_id": _id(),
                "span_type": "log",
                "status": "error" if level == "error" else "ok",
                "step_name": message,
                "payload_json": {"level": level, **fields} or None,
            }
        )


class _NoopSpan(Span):
    def __init__(self) -> None:  # noqa: D401 - trivial
        pass

    def log_model_call(self, *a: Any, **k: Any) -> None:  # noqa: D401
        pass

    def log_tool_call(self, *a: Any, **k: Any) -> None:  # noqa: D401
        pass

    def log(self, *a: Any, **k: Any) -> None:  # noqa: D401
        pass


_NOOP_SPAN = _NoopSpan()


def current_span() -> Span:
    """The span for the turn currently being handled, or a no-op span outside one.
    Always safe to call and chain (never returns None)."""
    return _current.get() or _NOOP_SPAN


class _TurnSpanCtx:
    """Context manager that brackets a turn span (started → completed/failed) and
    installs it as the current span for the duration."""

    def __init__(self, tracer: "Tracer", trace_id: str, base: Dict[str, Any]):
        self._t = tracer
        self._trace_id = trace_id
        self._base = base
        self._span: Optional[Span] = None
        self._token = None
        self._start = 0.0

    def __enter__(self) -> Span:
        span_id = _id()
        self._base = {**self._base, "span_id": span_id}
        self._span = Span(self._t, self._trace_id, span_id, self._base)
        self._t._emit({**self._base, "event_type": "span_started", "status": "ok"})
        self._start = time.perf_counter()
        self._token = _current.set(self._span)
        return self._span

    def __exit__(self, exc_type, exc, tb) -> bool:
        ms = int((time.perf_counter() - self._start) * 1000)
        end = {**self._base, "latency_ms": ms}
        if exc_type is not None:
            end.update(event_type="span_failed", status="error", error_message=str(exc))
        else:
            end.update(event_type="span_completed", status="ok")
        self._t._emit(end)
        if self._token is not None:
            _current.reset(self._token)
        return False  # never swallow the handler's exception


class Tracer:
    """Batching, non-blocking emitter to the Pyyol Lens ingest."""

    def __init__(
        self,
        endpoint: str,
        api_key: str,
        *,
        project: str = "pyyol-agents",
        environment: str = "development",
        service: str = "pyyol-agent",
        agent_id: str = "",
        flush_interval: float = 1.0,
        max_batch: int = 100,
        buffer_size: int = 2048,
        timeout: float = 5.0,
    ):
        self.enabled = bool(endpoint and api_key)
        self._endpoint = endpoint.rstrip("/") + "/v1/events/batch"
        self._key = api_key
        self._project = project
        self._env = environment
        self._service = service
        self._agent_id = agent_id
        self._flush_interval = flush_interval
        self._max_batch = max_batch
        self._timeout = timeout
        self._q: "queue.Queue[Dict[str, Any]]" = queue.Queue(maxsize=buffer_size)
        self._stop = threading.Event()
        self.dropped = 0
        self._thread: Optional[threading.Thread] = None
        if self.enabled:
            self._thread = threading.Thread(target=self._loop, name="pyyol-lens", daemon=True)
            self._thread.start()

    @classmethod
    def from_env(cls, *, agent_id: str = "", service: str = "pyyol-agent") -> "Tracer":
        return cls(
            endpoint=os.environ.get("PYYOL_LENS_ENDPOINT", ""),
            api_key=os.environ.get("PYYOL_LENS_API_KEY", ""),
            project=os.environ.get("PYYOL_LENS_PROJECT", "pyyol-agents"),
            environment=os.environ.get("PYYOL_LENS_ENV", "development"),
            service=service,
            agent_id=agent_id,
        )

    def turn_span(
        self, match_id: str, game: str = "", round_no: int = 0, agent_id: str = ""
    ) -> _TurnSpanCtx:
        """Bracket one agent turn. Correlated to the match trace via match_trace_id."""
        if not self.enabled:
            return _NoopTurnCtx()
        trace_id = match_trace_id(match_id) or _id()
        base = {
            "trace_id": trace_id,
            "request_id": trace_id,
            "step_name": "agent.turn",
            "span_type": "agent_turn",
            "actor_id": agent_id or self._agent_id,
            "run_id": match_id,
            "session_id": game,
            "payload_json": {"game": game, "round": round_no} if game or round_no else None,
        }
        return _TurnSpanCtx(self, trace_id, base)

    def _emit(self, ev: Dict[str, Any]) -> None:
        if not self.enabled or self._stop.is_set():
            return
        ev.setdefault("event_id", _id())
        ev.setdefault("event_time", _now_iso())
        ev.setdefault("schema_version", SCHEMA_VERSION)
        ev.setdefault("source_service", self._service)
        ev.setdefault("component", self._service)
        ev.setdefault("project_id", self._project)
        ev.setdefault("environment", self._env)
        # Drop None values so the ingest applies its own defaults.
        clean = {k: v for k, v in ev.items() if v is not None}
        try:
            self._q.put_nowait(clean)
        except queue.Full:
            self.dropped += 1

    def _loop(self) -> None:
        batch: List[Dict[str, Any]] = []
        while not self._stop.is_set():
            timeout = self._flush_interval
            try:
                batch.append(self._q.get(timeout=timeout))
            except queue.Empty:
                self._flush(batch)
                batch = []
                continue
            # Drain quickly up to max_batch.
            while len(batch) < self._max_batch:
                try:
                    batch.append(self._q.get_nowait())
                except queue.Empty:
                    break
            if len(batch) >= self._max_batch:
                self._flush(batch)
                batch = []
        self._flush(batch)

    def _flush(self, batch: List[Dict[str, Any]]) -> None:
        if not batch:
            return
        body = json.dumps({"events": batch}).encode("utf-8")
        req = _request.Request(
            self._endpoint,
            data=body,
            method="POST",
            headers={"Content-Type": "application/json", "X-Pyyol-Key": self._key},
        )
        for attempt in range(3):
            try:
                with _request.urlopen(req, timeout=self._timeout) as resp:  # noqa: S310
                    if 200 <= resp.status < 300:
                        return
            except Exception:  # noqa: BLE001 - telemetry must never raise into the app
                pass
            time.sleep(0.25 * (2 ** attempt))
        self.dropped += len(batch)

    def close(self, timeout: float = 3.0) -> None:
        if not self.enabled:
            return
        self._stop.set()
        if self._thread is not None:
            self._thread.join(timeout=timeout)


class _NoopTurnCtx(_TurnSpanCtx):
    def __init__(self) -> None:  # noqa: D401
        pass

    def __enter__(self) -> Span:
        return _NOOP_SPAN

    def __exit__(self, *a: Any) -> bool:
        return False


def _id() -> str:
    return uuid.uuid4().hex


def _now_iso() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime()) + "Z"
