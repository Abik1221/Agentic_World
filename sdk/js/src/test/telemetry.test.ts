import assert from "node:assert/strict";
import { createServer, type Server } from "node:http";
import { test } from "node:test";

import { Tracer, currentSpan, matchTraceId } from "../telemetry.js";

type Captured = Record<string, unknown>;

function captureServer(): Promise<{ url: string; events: Captured[]; key: () => string; close: () => Promise<void> }> {
  const events: Captured[] = [];
  let key = "";
  return new Promise((resolve) => {
    const srv: Server = createServer((req, res) => {
      key = String(req.headers["x-pyyol-key"] ?? "");
      let body = "";
      req.on("data", (c) => (body += c));
      req.on("end", () => {
        try {
          const parsed = JSON.parse(body) as { events?: Captured[] };
          if (parsed.events) events.push(...parsed.events);
        } catch {
          /* ignore */
        }
        res.writeHead(200).end("{}");
      });
    });
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      const port = typeof addr === "object" && addr ? addr.port : 0;
      resolve({
        url: `http://127.0.0.1:${port}`,
        events,
        key: () => key,
        close: () => new Promise((r) => srv.close(() => r())),
      });
    });
  });
}

test("turn span + model call reach the ingest, correlated to the match trace", async () => {
  const cap = await captureServer();
  const tr = new Tracer({ endpoint: cap.url, apiKey: "secret", flushIntervalMs: 20, agentId: "ag1" });
  assert.equal(tr.enabled, true);

  const result = await tr.runTurn({ matchId: "m42", game: "goofspiel", round: 2 }, () => {
    // Inside the handler, currentSpan() must resolve to the live turn span.
    currentSpan().logModelCall({ provider: "openai", model: "gpt-4o", promptTokens: 1000, completionTokens: 50, latencyMs: 600 });
    currentSpan().log("chose high card", { reason: "opp low" });
    return { card: 9 };
  });
  assert.deepEqual(result, { card: 9 }); // runTurn returns the handler's value

  await tr.close();

  const byType = new Map<string, Captured[]>();
  for (const e of cap.events) {
    const t = String(e.event_type);
    (byType.get(t) ?? byType.set(t, []).get(t)!).push(e);
  }
  await cap.close();

  assert.equal(cap.key(), "secret");
  for (const t of ["span_started", "span_completed", "model_call_completed", "log_record"]) {
    assert.ok(byType.has(t), `missing ${t}`);
  }
  // All correlated to the match trace.
  const want = matchTraceId("m42");
  for (const e of cap.events) assert.equal(e.trace_id, want);

  const mc = byType.get("model_call_completed")![0];
  assert.equal(mc.provider, "openai");
  assert.equal(mc.model, "gpt-4o");
  assert.equal(mc.total_tokens, 1050); // derived
  assert.equal(mc.actor_id, "ag1");
});

test("disabled tracer is a safe no-op", async () => {
  const tr = new Tracer({}); // no endpoint/key
  assert.equal(tr.enabled, false);
  const r = await tr.runTurn({ matchId: "m1", game: "goofspiel" }, () => {
    currentSpan().logModelCall({ model: "x" }); // must not throw
    currentSpan().log("hi");
    return 7;
  });
  assert.equal(r, 7);
  await tr.close();
});
