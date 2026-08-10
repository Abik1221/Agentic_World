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

test("gatewayBaseUrl resolves by WIRE FORMAT, not by a provider list", () => {
  enableGateway("agentA", "https://gateway.pyyol.com/");
  // Anthropic and Google clients append their own version segment, so the base must not.
  assert.equal(gatewayBaseUrl("anthropic"), "https://gateway.pyyol.com/gw/anthropic");
  assert.equal(gatewayBaseUrl("google"), "https://gateway.pyyol.com/gw/google");
  // Everything else is OpenAI-wire and needs /v1.
  assert.equal(gatewayBaseUrl("openai"), "https://gateway.pyyol.com/gw/openai/v1");
  for (const p of ["groq", "mistral", "deepseek", "cohere", "xai", "together", "openrouter"]) {
    assert.equal(gatewayBaseUrl(p), `https://gateway.pyyol.com/gw/${p}/v1`);
  }
});

test("an unlisted provider still routes", () => {
  // THE regression this guards. A provider->path table returned "" for anything unlisted, so
  // Gemini, Mistral, DeepSeek and every remote provider outside the table were NOT routed —
  // silently. Those agents produced no proofs and could never earn Verified, and nothing told
  // the developer. The ecosystem adds providers faster than a table can, so an unknown name
  // must route and let the gateway's own allowlist give a clear answer.
  enableGateway("agentA", "https://gateway.pyyol.com");
  assert.equal(
    gatewayBaseUrl("brand-new-provider"),
    "https://gateway.pyyol.com/gw/brand-new-provider/v1",
  );
});

test("a LOCAL provider is deliberately not routed", () => {
  // The gateway runs on Pyyol's side and cannot reach a model server on the developer's own
  // machine, so routing there would break every call. Such play is unverified — and free, so
  // there is no cost attribution to lose either.
  enableGateway("agentA", "https://gateway.pyyol.com");
  for (const p of ["ollama", "vllm", "lmstudio", "llamacpp", "self-hosted"]) {
    assert.equal(gatewayBaseUrl(p), "", `${p} must not be routed`);
  }
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
    _client = { baseURL: "https://gw/gw/openai/v1" }; // routed at the gateway
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
    _client = { baseURL: "https://gw/gw/openai/v1" }; // routed at the gateway
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

test("credential is NOT injected for a client not routed to the gateway", async () => {
  // Security regression: a client the dev forgot to route() points at a third-party
  // host; the X-Pyyol-Key credential must never be attached to that call.
  enableGateway("agentA", "https://gw");
  let seen: any = { present: true };
  class Completions {
    _client = { baseURL: "https://api.openai.com/v1" }; // NOT the gateway
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
  assert.equal(seen, undefined); // options arg never populated → no credential leaked
});
