"""End-to-end wire test for the SDK's Pyyol Lens tracer: a real local HTTP server
captures the batched events and we assert the shape + match correlation."""

from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from pyyol.telemetry import Tracer, current_span, match_trace_id


class _Capture(BaseHTTPRequestHandler):
    events: list = []
    key: str = ""

    def do_POST(self):  # noqa: N802
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length)
        _Capture.key = self.headers.get("X-Pyyol-Key", "")
        _Capture.events.extend(json.loads(body).get("events", []))
        self.send_response(200)
        self.end_headers()

    def log_message(self, *a):  # silence
        pass


@pytest.fixture()
def server():
    _Capture.events = []
    _Capture.key = ""
    httpd = HTTPServer(("127.0.0.1", 0), _Capture)
    t = threading.Thread(target=httpd.serve_forever, daemon=True)
    t.start()
    yield f"http://127.0.0.1:{httpd.server_address[1]}"
    httpd.shutdown()


def test_turn_span_and_model_call_reach_ingest(server):
    tr = Tracer(endpoint=server, api_key="secret", flush_interval=0.05, agent_id="ag1")
    assert tr.enabled

    with tr.turn_span(match_id="m42", game="goofspiel", round_no=2) as span:
        assert current_span() is span  # installed as current for handler code
        span.log_model_call(
            provider="openai",
            model="gpt-4o",
            prompt_tokens=1000,
            completion_tokens=50,
            latency_ms=600,
        )
        span.log("chose high card", reason="opp low")
    tr.close()

    by_type = {}
    for e in _Capture.events:
        by_type.setdefault(e["event_type"], []).append(e)

    assert _Capture.key == "secret"
    assert "span_started" in by_type
    assert "span_completed" in by_type
    assert "model_call_completed" in by_type
    assert "log_record" in by_type

    want_trace = match_trace_id("m42")
    for e in _Capture.events:
        assert e["trace_id"] == want_trace, e
        assert e["source_service"]  # defaulted
        assert e["event_id"]

    mc = by_type["model_call_completed"][0]
    assert mc["provider"] == "openai" and mc["model"] == "gpt-4o"
    assert mc["total_tokens"] == 1050  # derived
    assert mc["actor_id"] == "ag1"


def test_disabled_tracer_is_noop():
    tr = Tracer(endpoint="", api_key="")
    assert not tr.enabled
    with tr.turn_span(match_id="m1", game="goofspiel") as span:
        span.log_model_call(model="x")  # must not raise
        span.log("hi")
    tr.close()
    assert current_span() is not None  # noop span, never None
