import http.server
import threading

from pyyol import cli


def _serve(handler):
    srv = http.server.HTTPServer(("127.0.0.1", 0), handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, f"http://127.0.0.1:{srv.server_address[1]}"


def test_api_get_retries_after_429_then_succeeds():
    state = {"calls": 0}

    class H(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_a):
            pass

        def do_GET(self):
            state["calls"] += 1
            if state["calls"] == 1:
                # First hit is rate-limited; Retry-After 0 keeps the test fast.
                self.send_response(429)
                self.send_header("Retry-After", "0")
                self.send_header("Content-Length", "0")
                self.end_headers()
            else:
                body = b'{"ok": true}'
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

    srv, base = _serve(H)
    try:
        st, resp = cli._api_get(f"{base}/thing")
    finally:
        srv.shutdown()
    assert st == 200 and resp == {"ok": True}
    assert state["calls"] == 2  # retried exactly once after the 429


def test_api_get_gives_up_after_persistent_429():
    state = {"calls": 0}

    class H(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_a):
            pass

        def do_GET(self):
            state["calls"] += 1
            self.send_response(429)
            self.send_header("Retry-After", "0")
            self.send_header("Content-Length", "0")
            self.end_headers()

    srv, base = _serve(H)
    try:
        st, _ = cli._api_get(f"{base}/thing")
    finally:
        srv.shutdown()
    assert st == 429  # returns the 429 after exhausting retries
    assert state["calls"] == 3  # 1 initial + 2 retries, then stop
