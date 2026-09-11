import { test } from "node:test";
import assert from "node:assert/strict";
import { PyyolLens } from "../src/index";

test("traced emits span_started then span_completed and flushes a batch", async () => {
  const bodies: unknown[] = [];
  const orig = globalThis.fetch;
  globalThis.fetch = (async (_url: string | URL | Request, init?: RequestInit) => {
    bodies.push(JSON.parse(String(init?.body ?? "{}")));
    return new Response("{}", { status: 200 });
  }) as typeof fetch;

  const lens = new PyyolLens({
    apiKey: "test-key",
    endpoint: "http://127.0.0.1:9",
    serviceName: "sdk-test",
    project: "test",
    environment: "test",
    flushIntervalMs: 60_000,
    maxBatchSize: 50,
  });

  const result = await lens.traced("work", async () => 42);
  assert.equal(result, 42);
  await lens.shutdown();
  globalThis.fetch = orig;

  assert.ok(bodies.length >= 1);
  const events = (bodies[0] as { events: Array<{ event_type: string; step_name?: string }> }).events;
  const types = events.map((e) => e.event_type);
  assert.ok(types.includes("span_started"));
  assert.ok(types.includes("span_completed"));
  assert.equal(events[0].step_name, "work");
});

test("redacts secrets in payload_json", async () => {
  const bodies: Array<{ events: Array<{ payload_json?: Record<string, unknown> }> }> = [];
  const orig = globalThis.fetch;
  globalThis.fetch = (async (_url: string | URL | Request, init?: RequestInit) => {
    bodies.push(JSON.parse(String(init?.body ?? "{}")));
    return new Response("{}", { status: 200 });
  }) as typeof fetch;

  const lens = new PyyolLens({
    apiKey: "test-key",
    endpoint: "http://127.0.0.1:9",
    serviceName: "sdk-test",
    project: "test",
    environment: "test",
    flushIntervalMs: 60_000,
  });
  lens.emit({
    event_type: "note",
    payload_json: { ok: true, api_key: "super-secret" },
  });
  await lens.shutdown();
  globalThis.fetch = orig;

  const payload = bodies[0].events[0].payload_json ?? {};
  assert.equal(payload.ok, true);
  assert.equal(payload.api_key, "[redacted]");
});
