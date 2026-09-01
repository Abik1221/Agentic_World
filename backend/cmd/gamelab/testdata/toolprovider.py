"""Stand-in LLM provider for Pyyol lab runs.

WHAT THIS IS NOT: a model. Every completion here is derived from the prompt by string
parsing. Data produced through this provider exercises the gateway, turn proof, completion
binding and the engines — it must NEVER be published as a model benchmark, because there is
no model in the loop. Real benchmarks require a real provider key.

WHAT IT MUST GET RIGHT: the wire shape. The gateway reads the RESPONSE, so a response in the
wrong shape would make binding look broken when it is not. Two shapes are served:

  POST /v1/messages           Anthropic  → content[] with a tool_use block
  POST /v1/chat/completions   OpenAI     → choices[].message.tool_calls[]

Both support stream=true, because the SSE tool-call reassembly path is one of the things the
lab exists to exercise (-bind-stream).

WHAT DECIDES THE MOVE. The harness tells this provider what to answer, via headers documented
in gamelab's bind.go: X-Lab-Card (one round), X-Lab-Plan (a batched multi-round plan) and
X-Lab-Kind. The lab persona's OWN strategy chooses; the provider only says it. That is what
makes a bound run play identically to an unbound one, so the two are comparable — and it is
what gives the personas different play, hence real winners and losers.

Only when no header is sent does it fall back to deriving something legal from the prompt:

  - Anthropic goofspiel prompt: "Round 7. Choose a card ..."  → card 7. Legal because the lab
    hand is sequential (1..13) and card N is still held at round N.
  - OpenAI goofspiel prompt: "Your legal cards are [11 12 13]" → first listed.
  - Mafia/Monopoly: "Legal actions: vote, pass, ..." → the first listed kind.

The fallback is deliberately dull, because an illegal move looks exactly like a binding failure
in the run log. It is also NOT a substitute for the headers: with every persona deriving the
same card from the same prompt, every round ties and a whole batch finishes 0-0 — mechanically
valid, statistically worthless.
"""

import json
import re
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = 8099


def _round_from(text):
    m = re.search(r"[Rr]ound\s+(\d+)", text)
    return int(m.group(1)) if m else 1


def _legal_cards(text):
    """Cards from "legal cards are [11 12 13]" — the OpenAI-wire prompt."""
    m = re.search(r"legal cards are \[([0-9 ,]+)\]", text)
    if not m:
        return []
    return [int(x) for x in re.findall(r"\d+", m.group(1))]


def _legal_actions(text):
    """Kinds from "Legal actions: a, b, c." — the mafia/monopoly prompt."""
    m = re.search(r"Legal actions:\s*(.+?)\.\s", text + " ")
    if not m:
        return []
    return [p.strip() for p in m.group(1).split(",") if p.strip()]


def _tool_spec(body):
    """(name, schema) for the single requested tool, in either wire shape."""
    tools = body.get("tools") or []
    if not tools:
        return "", {}
    t = tools[0]
    if "function" in t:  # OpenAI
        fn = t["function"] or {}
        return fn.get("name", ""), fn.get("parameters") or {}
    return t.get("name", ""), t.get("input_schema") or {}


def _prompt_text(body):
    out = []
    for m in body.get("messages") or []:
        c = m.get("content")
        if isinstance(c, str):
            out.append(c)
        elif isinstance(c, list):
            for part in c:
                if isinstance(part, dict) and isinstance(part.get("text"), str):
                    out.append(part["text"])
    return "\n".join(out)


def _tool_input(name, schema, text, headers):
    """The tool arguments. Shaped by the SCHEMA the caller sent, not by a guess at it —
    goofspiel switches between a single `card` and a multi-round `plan` and the caller is
    the only one who knows which.

    X-Lab-Card / X-Lab-Plan OUTRANK everything derived from the prompt. That is the harness's
    documented contract (see gamelab bind.go): the lab persona's own strategy chooses the
    card, and this provider is made to say what the strategy decided, so a bound run plays
    identically to an unbound one and the two are comparable.

    Ignoring those headers is not a small inaccuracy — it makes every persona play the same
    derived card, every round ties, and a whole batch of matches ends 0-0 with no winner. A
    dataset like that cannot produce a rating or a benchmark. Prompt derivation below is only
    the fallback for a caller that sends no header.
    """
    props = (schema or {}).get("properties") or {}

    if "plan" in props:  # goofspiel, multi-round one-call form
        span = int((props["plan"] or {}).get("maxItems") or 1)
        start = _round_from(text)
        # "3:11,4:2,5:9" — the rounds this one completion decides, exactly as the strategy
        # planned them. The gateway binds every round in the plan from this single call.
        planned = headers.get("X-Lab-Plan") or ""
        steps = []
        for pair in planned.split(","):
            if ":" in pair:
                r, c = pair.split(":", 1)
                if r.strip().isdigit() and c.strip().isdigit():
                    steps.append({"round": int(r), "card": int(c)})
        if steps:
            return {"plan": steps}
        legal = _legal_cards(text)
        return {"plan": [
            {"round": start + i, "card": legal[i] if i < len(legal) else start + i}
            for i in range(span)
        ]}

    if "card" in props:  # goofspiel, single round
        want = headers.get("X-Lab-Card")
        if want and want.strip().lstrip("-").isdigit():
            return {"card": int(want)}
        legal = _legal_cards(text)
        return {"card": legal[0] if legal else _round_from(text)}

    # mafia / monopoly: a `kind` plus whatever optional fields the schema offers.
    # `target` is deliberately OMITTED, never sent as 0 — seat 0 is a real player and a
    # defaulted target would bind an action against someone the caller never named.
    actions = _legal_actions(text)
    want_kind = (headers.get("X-Lab-Kind") or "").strip()
    args = {"kind": want_kind or (actions[0] if actions else "pass")}
    if "text" in props:
        args["text"] = "Holding for now; watching the table."
    if "rationale" in props:
        args["rationale"] = "Cheapest legal action while the board develops."
    return args


def _usage_anthropic(text, args):
    return {"input_tokens": max(1, len(text) // 4), "output_tokens": max(1, len(json.dumps(args)) // 4)}


def _usage_openai(text, args):
    p = max(1, len(text) // 4)
    c = max(1, len(json.dumps(args)) // 4)
    return {"prompt_tokens": p, "completion_tokens": c, "total_tokens": p + c}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *a):  # quiet: the run log is the interesting one
        pass

    def _send(self, code, payload, ctype="application/json"):
        raw = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        if self.path.rstrip("/").endswith("health"):
            return self._send(200, {"ok": True, "stand_in": True})
        return self._send(404, {"error": "not found"})

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        try:
            body = json.loads(self.rfile.read(n) or b"{}")
        except json.JSONDecodeError:
            return self._send(400, {"error": {"message": "malformed JSON"}})

        name, schema = _tool_spec(body)
        text = _prompt_text(body)
        args = _tool_input(name, schema, text, self.headers)
        model = body.get("model") or "stand-in"
        anthropic = "/v1/messages" in self.path

        if body.get("stream"):
            return self._stream(anthropic, name, args, model, text)

        if anthropic:
            return self._send(200, {
                "id": "msg_standin", "type": "message", "role": "assistant", "model": model,
                "content": [{"type": "tool_use", "id": "toolu_standin", "name": name, "input": args}],
                "stop_reason": "tool_use",
                "usage": _usage_anthropic(text, args),
            })
        return self._send(200, {
            "id": "chatcmpl_standin", "object": "chat.completion", "model": model,
            "choices": [{
                "index": 0, "finish_reason": "tool_calls",
                "message": {"role": "assistant", "content": None, "tool_calls": [{
                    "id": "call_standin", "type": "function",
                    "function": {"name": name, "arguments": json.dumps(args)},
                }]},
            }],
            "usage": _usage_openai(text, args),
        })

    def _stream(self, anthropic, name, args, model, text):
        """SSE. The arguments are split across TWO deltas on purpose: a reassembler that
        only reads the first fragment passes a single-delta stream and fails here, which is
        the whole reason -bind-stream exists."""
        blob = json.dumps(args)
        half = len(blob) // 2 or 1
        parts = [blob[:half], blob[half:]]
        frames = []

        def ev(event, data):
            if anthropic:
                frames.append(f"event: {event}\ndata: {json.dumps(data)}\n\n")
            else:
                frames.append(f"data: {json.dumps(data)}\n\n")

        if anthropic:
            ev("message_start", {"type": "message_start", "message": {
                "id": "msg_standin", "model": model, "role": "assistant", "content": [],
                "usage": _usage_anthropic(text, args)}})
            ev("content_block_start", {"type": "content_block_start", "index": 0,
                "content_block": {"type": "tool_use", "id": "toolu_standin", "name": name, "input": {}}})
            for p in parts:
                ev("content_block_delta", {"type": "content_block_delta", "index": 0,
                    "delta": {"type": "input_json_delta", "partial_json": p}})
            ev("content_block_stop", {"type": "content_block_stop", "index": 0})
            ev("message_delta", {"type": "message_delta", "delta": {"stop_reason": "tool_use"},
                "usage": _usage_anthropic(text, args)})
            ev("message_stop", {"type": "message_stop"})
        else:
            for i, p in enumerate(parts):
                call = {"index": 0, "function": {"arguments": p}}
                if i == 0:
                    call.update({"id": "call_standin", "type": "function"})
                    call["function"]["name"] = name
                ev("delta", {"id": "chatcmpl_standin", "object": "chat.completion.chunk",
                    "model": model, "choices": [{"index": 0, "delta": {"tool_calls": [call]}}]})
            ev("done", {"id": "chatcmpl_standin", "object": "chat.completion.chunk", "model": model,
                "choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls"}],
                "usage": _usage_openai(text, args)})
            frames.append("data: [DONE]\n\n")

        raw = "".join(frames).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
