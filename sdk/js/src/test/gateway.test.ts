import { test, afterEach } from "node:test";
import assert from "node:assert/strict";

import {
  disableGateway,
  enableGateway,
  gatewayBaseUrl,
  gatewayHeaders,
  patchPrototype,
  route,
  uninstrument,
} from "../instrument.js";
import { runTurnUsage } from "../telemetry.js";

afterEach(() => {
  disableGateway();
  uninstrument();
});

test("gatewayBaseUrl resolves per provider", () => {
  enableGateway("agentA", "https://gateway.pyyol.com/");
  assert.equal(gatewayBaseUrl("openai"), "https://gateway.pyyol.com/gw/openai/v1");
  assert.equal(gatewayBaseUrl("anthropic"), "https://gateway.pyyol.com/gw/anthropic");
  assert.equal(gatewayBaseUrl("unknown"), "");
});

test("gatewayBaseUrl empty when disabled", () => {
  assert.equal(gatewayBaseUrl("openai"), "");
});

test("gatewayHeaders empty when disabled", () => {
  assert.deepEqual(gatewayHeaders(), {});
});

test("gatewayHeaders reads turn context", async () => {
  enableGateway("agentA", "https://gw");
  await runTurnUsage(
    async () => {
      const h = gatewayHeaders();
      assert.equal(h["X-Pyyol-Key"], "agentA");
      assert.equal(h["X-Pyyol-Match"], "m42");
      assert.equal(h["X-Pyyol-Turn"], "3");
    },
    { matchId: "m42", turn: 3 },
  );
});

test("gatewayHeaders without a match", () => {
  enableGateway("agentA", "https://gw");
  assert.deepEqual(gatewayHeaders(), { "X-Pyyol-Key": "agentA" });
});

test("route sets baseURL and detects provider by constructor name", () => {
  enableGateway("agentA", "https://gw");
  class OpenAI {
    baseURL = "https://api.openai.com/v1";
  }
  const c = new OpenAI();
  route(c);
  assert.equal(c.baseURL, "https://gw/gw/openai/v1");
});

test("route no-op when disabled", () => {
  class OpenAI {
    baseURL = "orig";
  }
  const c = new OpenAI();
  route(c);
  assert.equal(c.baseURL, "orig");
});

test("instrumented call injects headers into the options arg (dev wins)", async () => {
  enableGateway("agentA", "https://gw");
  let seen: any;
  class Completions {
    async create(_body: unknown, options?: unknown): Promise<unknown> {
      seen = options;
      return { model: "gpt-4o", usage: { prompt_tokens: 10, completion_tokens: 5 } };
    }
  }
  patchPrototype(Completions.prototype, "create", "openai");
  const client = new Completions();
  await runTurnUsage(
    async () => {
      await client.create({ model: "gpt-4o" }, { headers: { "X-Pyyol-Key": "dev-wins" } });
    },
    { matchId: "m7", turn: 2 },
  );
  assert.equal(seen.headers["X-Pyyol-Match"], "m7");
  assert.equal(seen.headers["X-Pyyol-Turn"], "2");
  assert.equal(seen.headers["X-Pyyol-Key"], "dev-wins"); // dev-supplied overrides
});

test("instrumented call creates the options arg when absent", async () => {
  enableGateway("agentA", "https://gw");
  let seen: any;
  class Completions {
    async create(_body: unknown, options?: unknown): Promise<unknown> {
      seen = options;
      return { model: "gpt-4o", usage: { prompt_tokens: 1, completion_tokens: 1 } };
    }
  }
  patchPrototype(Completions.prototype, "create", "openai");
  const client = new Completions();
  await runTurnUsage(
    async () => {
      await client.create({ model: "gpt-4o" });
    },
    { matchId: "m1", turn: 1 },
  );
  assert.equal(seen.headers["X-Pyyol-Key"], "agentA");
});
