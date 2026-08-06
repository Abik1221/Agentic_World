import { test } from "node:test";
import assert from "node:assert/strict";

import { Agent } from "../server.js";
import { RuntimeConnector, type WebSocketLike } from "../runtime.js";
import { extractUsage, instrument, patchPrototype, recordResponse, uninstrument } from "../instrument.js";
import { runTurnUsage } from "../telemetry.js";

// --- fake provider responses ---------------------------------------------------

function openaiChat(model = "gpt-4o", prompt = 1200, completion = 80, cached = 0, reasoning = 0) {
  return {
    model,
    usage: {
      prompt_tokens: prompt,
      completion_tokens: completion,
      total_tokens: prompt + completion,
      prompt_tokens_details: { cached_tokens: cached },
      completion_tokens_details: { reasoning_tokens: reasoning },
    },
  };
}

function anthropic(model = "claude-sonnet-4-5", inp = 900, out = 120, cacheRead = 0, cacheWrite = 0) {
  return {
    model,
    usage: {
      input_tokens: inp,
      output_tokens: out,
      cache_read_input_tokens: cacheRead,
      cache_creation_input_tokens: cacheWrite,
    },
  };
}

function responsesApi(model = "gpt-4.1", inp = 500, out = 40) {
  return { model, usage: { input_tokens: inp, output_tokens: out } };
}

// --- extractUsage --------------------------------------------------------------

test("extract OpenAI chat usage", () => {
  const info = extractUsage(openaiChat("gpt-4o", 1200, 80, 300, 20));
  assert.deepEqual(info, {
    model: "gpt-4o",
    provider: "openai",
    promptTokens: 1200,
    completionTokens: 80,
    cachedTokens: 300,
    cachedWriteTokens: 0,
    reasoningTokens: 20,
  });
});

test("OpenAI cached tokens stay a subset and are not double counted", () => {
  // The mirror image of the Anthropic case. OpenAI reports cached tokens INSIDE
  // prompt_tokens, so adding them would inflate billable input — the normalization that
  // fires for Anthropic must not fire here.
  const info = extractUsage(openaiChat("gpt-4o", 1200, 80, 300, 0))!;
  assert.equal(info.promptTokens, 1200);
  assert.equal(info.cachedTokens, 300);
});

test("extract Anthropic usage", () => {
  const info = extractUsage(anthropic("claude-sonnet-4-5", 900, 120, 100))!;
  assert.equal(info.provider, "anthropic");
  // 900 uncached + 100 cache reads. Anthropic's `input_tokens` counts only the uncached
  // remainder, so the cache fields are ADDED to recover billable input — unlike OpenAI,
  // where prompt_tokens already contains them.
  assert.equal(info.promptTokens, 1000);
  assert.equal(info.completionTokens, 120);
  assert.equal(info.cachedTokens, 100);
});

test("extract Anthropic cache writes", () => {
  // Cache CREATION tokens are billed at 1.25x input and were previously not read at all,
  // so a cache-heavy agent's most expensive tokens were recorded as zero.
  const info = extractUsage(anthropic("claude-opus-4", 420, 90, 1500, 600))!;
  assert.equal(info.cachedWriteTokens, 600);
  assert.equal(info.cachedTokens, 1500);
  // Every billable input token accounted for: 420 uncached + 1500 read + 600 written.
  assert.equal(info.promptTokens, 2520);
  // The invariant pricing depends on: the cache portions never exceed the input total.
  assert.ok(info.cachedTokens + info.cachedWriteTokens <= info.promptTokens);
});

test("extract Responses API usage", () => {
  const info = extractUsage(responsesApi())!;
  assert.equal(info.promptTokens, 500);
  assert.equal(info.completionTokens, 40);
  assert.equal(info.model, "gpt-4.1");
});

test("extract returns null without usage", () => {
  assert.equal(extractUsage({ model: "gpt-4o" }), null);
  assert.equal(extractUsage({}), null);
});

// --- recordResponse: accumulation + cost ---------------------------------------

test("recordResponse accumulates and costs", async () => {
  const { usage } = await runTurnUsage(async () => {
    recordResponse(openaiChat("gpt-4o", 1000, 500));
  });
  assert.equal(usage.calls, 1);
  assert.equal(usage.promptTokens, 1000);
  assert.equal(usage.completionTokens, 500);
  assert.equal(usage.totalTokens, 1500);
  assert.ok(Math.abs(usage.estimatedCost - (1000 * 2.5 + 500 * 10) / 1_000_000) < 1e-9);
  assert.deepEqual(usage.models, ["gpt-4o"]);
});

test("recordResponse sums multiple calls in a turn", async () => {
  const { usage } = await runTurnUsage(async () => {
    recordResponse(openaiChat("gpt-4o", 100, 10));
    recordResponse(anthropic("claude-sonnet-4-5", 200, 20));
  });
  assert.equal(usage.calls, 2);
  assert.equal(usage.promptTokens, 300);
  assert.equal(usage.completionTokens, 30);
  assert.deepEqual(new Set(usage.providers), new Set(["openai", "anthropic"]));
});

test("toMoveUsage matches arena contract keys", async () => {
  const { usage } = await runTurnUsage(async () => {
    recordResponse(openaiChat("gpt-4o", 100, 50, 0, 10));
  });
  const m = usage.toMoveUsage();
  assert.equal(m.prompt_tokens, 100);
  assert.equal(m.completion_tokens, 50);
  assert.equal(m.total_tokens, 150);
  assert.equal(m.reasoning_tokens, 10);
  assert.equal(m.model, "gpt-4o");
  assert.ok((m.estimated_cost ?? 0) > 0);
});

test("recordResponse outside a turn is a safe no-op", () => {
  const info = recordResponse(openaiChat());
  assert.ok(info !== null);
});

// --- patchPrototype ------------------------------------------------------------

test("patchPrototype captures calls; uninstrument restores", async () => {
  class FakeCompletions {
    async create(_args: unknown): Promise<unknown> {
      return openaiChat("gpt-4o", 1000, 100);
    }
  }
  assert.equal(patchPrototype(FakeCompletions.prototype, "create", "openai"), true);
  // idempotent
  assert.equal(patchPrototype(FakeCompletions.prototype, "create", "openai"), false);

  const client = new FakeCompletions();
  const { usage } = await runTurnUsage(async () => {
    const resp = (await client.create({ model: "gpt-4o" })) as ReturnType<typeof openaiChat>;
    assert.equal(resp.usage.prompt_tokens, 1000); // response returned untouched
  });
  assert.equal(usage.calls, 1);
  assert.equal(usage.promptTokens, 1000);
  assert.equal(usage.completionTokens, 100);

  uninstrument();
  const { usage: after } = await runTurnUsage(async () => {
    await client.create({ model: "gpt-4o" });
  });
  assert.equal(after.calls, 0);
});

test("instrument skips unknown / absent providers without error", async () => {
  assert.deepEqual(await instrument(["definitely-not-a-provider"]), []);
  // openai/anthropic aren't installed in the SDK dev env → skipped gracefully.
  const done = await instrument();
  assert.ok(Array.isArray(done));
  uninstrument();
});

// --- end-to-end: auto-attach usage to the move through the runtime -------------

class FakeWS implements WebSocketLike {
  readyState = 1;
  sent: any[] = [];
  private listeners: Record<string, ((ev: any) => void)[]> = {};
  constructor(incoming: any[]) {
    queueMicrotask(async () => {
      for (const f of incoming) {
        this.emit("message", { data: JSON.stringify(f) });
        await Promise.resolve();
      }
      this.emit("close", {});
    });
  }
  send(data: string) {
    this.sent.push(JSON.parse(data));
  }
  close() {
    this.readyState = 3;
  }
  addEventListener(type: string, listener: (ev: any) => void) {
    (this.listeners[type] ??= []).push(listener);
  }
  private emit(type: string, ev: any) {
    for (const l of this.listeners[type] ?? []) l(ev);
  }
}

async function runTurn(agent: Agent): Promise<any> {
  let ws!: FakeWS;
  const conn = new RuntimeConnector(agent, {
    url: "ws://x",
    agentId: "ag",
    token: "s",
    games: ["goofspiel"],
    reconnect: false,
    WebSocketImpl: class extends FakeWS {
      constructor(_u: string) {
        super([
          { t: "hello", version: "1.0" },
          { t: "registered", agent_id: "ag" },
          { t: "turn", id: "r1", payload: { game: "goofspiel", match_id: "m1", round: 2, legal_actions: [3, 7, 9] } },
        ]);
        ws = this;
      }
    } as any,
  });
  await conn.run();
  return ws.sent.find((f) => f.t === "response" && f.id === "r1");
}

test("runtime auto-attaches captured usage to the move", async () => {
  class FakeCompletions {
    async create(_a: unknown): Promise<unknown> {
      return openaiChat("gpt-4o", 1000, 100);
    }
  }
  patchPrototype(FakeCompletions.prototype, "create", "openai");
  const client = new FakeCompletions();

  const agent = new Agent({ supportedGames: ["goofspiel"], name: "t" });
  agent.onTurn("goofspiel", async (v: any) => {
    await client.create({ model: "gpt-4o", messages: [] });
    return { round: v.round, card: Math.max(...v.legal_actions) };
  });

  const resp = await runTurn(agent);
  uninstrument();
  assert.equal(resp.payload.card, 9); // move preserved
  assert.equal(resp.payload.usage.prompt_tokens, 1000);
  assert.equal(resp.payload.usage.completion_tokens, 100);
  assert.equal(resp.payload.usage.total_tokens, 1100);
  assert.equal(resp.payload.usage.model, "gpt-4o");
  assert.ok(resp.payload.usage.estimated_cost > 0);
});

test("runtime respects dev-supplied usage", async () => {
  class FakeCompletions {
    async create(_a: unknown): Promise<unknown> {
      return openaiChat("gpt-4o", 1000, 100);
    }
  }
  patchPrototype(FakeCompletions.prototype, "create", "openai");
  const client = new FakeCompletions();

  const agent = new Agent({ supportedGames: ["goofspiel"], name: "t" });
  agent.onTurn("goofspiel", async (v: any) => {
    await client.create({ model: "gpt-4o" });
    return { round: v.round, card: 3, usage: { prompt_tokens: 42, completion_tokens: 0, total_tokens: 42 } };
  });

  const resp = await runTurn(agent);
  uninstrument();
  assert.equal(resp.payload.usage.prompt_tokens, 42);
});

test("runtime attaches no usage when no LLM call is made", async () => {
  const agent = new Agent({ supportedGames: ["goofspiel"], name: "t" });
  agent.onTurn("goofspiel", (v: any) => ({ round: v.round, card: 7 }));
  const resp = await runTurn(agent);
  assert.equal("usage" in resp.payload, false);
});
