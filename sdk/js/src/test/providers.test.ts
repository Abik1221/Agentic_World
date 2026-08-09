// Whatever the developer runs, the SDK must identify it.
//
// Most of the ecosystem speaks the OpenAI wire format, so the OpenAI SDK pointed at a
// different baseURL is the single most common way to run anything: Ollama, vLLM,
// LM Studio, llama.cpp, OpenRouter, Together, Groq, DeepSeek, Azure. Classifying those
// by client class calls all of them "openai", which prices a model running on the
// developer's own GPU at OpenAI's rates and files it under the wrong vendor on a
// public leaderboard.

import { test } from "node:test";
import assert from "node:assert/strict";

import { fromBaseUrl, fromName, isSelfHosted, resolve } from "../providers.js";
import { estimateCost } from "../pricing.js";
import { disableGateway, enableGateway, extractUsage, resolveCallProvider } from "../instrument.js";

test("hosted providers are identified by host", () => {
  const cases: Array<[string, string]> = [
    ["https://api.openai.com/v1", "openai"],
    ["https://api.anthropic.com", "anthropic"],
    ["https://api.groq.com/openai/v1", "groq"],
    ["https://openrouter.ai/api/v1", "openrouter"],
    ["https://api.together.xyz/v1", "together"],
    ["https://api.deepseek.com/v1", "deepseek"],
    ["https://api.x.ai/v1", "xai"],
    ["https://my-org.openai.azure.com/", "azure"],
    ["https://generativelanguage.googleapis.com/v1beta", "google"],
    ["https://bedrock-runtime.us-east-1.amazonaws.com", "bedrock"],
  ];
  for (const [url, want] of cases) {
    assert.equal(fromBaseUrl(url), want, url);
  }
});

test("local runtimes are identified as self-hosted", () => {
  const cases: Array<[string, string]> = [
    ["http://localhost:11434/v1", "ollama"],
    ["http://127.0.0.1:11434", "ollama"],
    ["http://localhost:1234/v1", "lmstudio"],
    ["http://localhost:8000/v1", "vllm"],
    ["http://localhost:8080/v1", "llamacpp"],
    // An unrecognised port on a local address is STILL self-hosted: "we could not name
    // the runtime" and "this costs money" are different claims.
    ["http://localhost:7777/v1", "self-hosted"],
    ["http://192.168.1.50:9999/v1", "self-hosted"],
    ["http://10.0.0.4:8123/v1", "self-hosted"],
    ["http://172.16.5.5:9000/v1", "self-hosted"],
    ["http://host.docker.internal:11434", "ollama"],
  ];
  for (const [url, want] of cases) {
    assert.equal(fromBaseUrl(url), want, url);
    assert.ok(isSelfHosted(fromBaseUrl(url)), `${url} should be self-hosted`);
  }
});

test("an unknown public host is not guessed into a provider", () => {
  assert.equal(fromBaseUrl("https://llm.some-startup.example/v1"), "");
});

test("baseURL beats the client name", () => {
  // The decisive case: an OpenAI CLIENT talking to Ollama is Ollama. Resolving by name
  // would report "openai" and bill for tokens OpenAI never served.
  assert.equal(resolve({ name: "OpenAI", baseUrl: "http://localhost:11434/v1" }), "ollama");
  assert.equal(resolve({ name: "OpenAI", baseUrl: "https://api.openai.com/v1" }), "openai");
});

test("client name is used when there is no baseURL", () => {
  assert.equal(fromName("Anthropic"), "anthropic");
  assert.equal(fromName("Ollama"), "ollama");
  assert.equal(resolve({ name: "OpenAI" }), "openai");
});

test("resolution never throws on junk", () => {
  for (const junk of ["", "not a url", "://///", "http://"]) {
    assert.doesNotThrow(() => resolve({ baseUrl: junk }));
  }
});

test("self-hosted models are free, the same model on a host is not", () => {
  const hosted = estimateCost("llama-3.3-70b", {
    promptTokens: 1_000_000,
    completionTokens: 1_000_000,
    provider: "groq",
  });
  const local = estimateCost("llama-3.3-70b", {
    promptTokens: 1_000_000,
    completionTokens: 1_000_000,
    provider: "ollama",
  });
  assert.ok(hosted > 0, "a hosted open-weight model has a real bill");
  assert.equal(local, 0, "a self-hosted model has no per-token bill");
});

test("an unknown model served locally is free, not fallback-priced", () => {
  // Without the self-hosted check this falls through to the mid-tier fallback and
  // charges a developer for their own hardware.
  assert.equal(
    estimateCost("brand-new-thing-2026", { promptTokens: 500_000, completionTokens: 500_000, provider: "ollama" }),
    0,
  );
  assert.ok(
    estimateCost("brand-new-thing-2026", {
      promptTokens: 500_000,
      completionTokens: 500_000,
      provider: "openrouter",
    }) > 0,
  );
});

test("ollama's native response shape is extracted", () => {
  const info = extractUsage({ model: "llama3.3:70b", prompt_eval_count: 120, eval_count: 45, done: true });
  assert.ok(info, "an Ollama response must not read as 'no usage'");
  assert.equal(info!.provider, "ollama");
  assert.equal(info!.promptTokens, 120);
  assert.equal(info!.completionTokens, 45);
});

test("gemini's usageMetadata shape is extracted", () => {
  const info = extractUsage({
    modelVersion: "gemini-2.5-pro",
    usageMetadata: { promptTokenCount: 800, candidatesTokenCount: 200, cachedContentTokenCount: 100 },
  });
  assert.ok(info);
  assert.equal(info!.provider, "google");
  assert.equal(info!.promptTokens, 800);
  assert.equal(info!.cachedTokens, 100);
});

test("a normal openai response is never hijacked by the new shapes", () => {
  const info = extractUsage({
    model: "gpt-4o",
    usage: { prompt_tokens: 10, completion_tokens: 5 },
    // Decoys from the other shapes.
    prompt_eval_count: 9999,
    usageMetadata: { promptTokenCount: 9999 },
  });
  assert.equal(info!.provider, "openai");
  assert.equal(info!.promptTokens, 10);
});

test("a response with no usage anywhere returns null", () => {
  assert.equal(extractUsage({ model: "gpt-4o", choices: [] }), null);
});

test("call-site resolution reads the client's baseURL", () => {
  const resource = (baseURL: string | null) => ({ _client: { baseURL } });
  // Patched as the OpenAI SDK, actually pointed at a local LM Studio.
  assert.equal(resolveCallProvider(resource("http://localhost:1234/v1"), "openai"), "lmstudio");
  assert.equal(resolveCallProvider(resource("https://api.openai.com/v1"), "openai"), "openai");
  // No readable baseURL falls back to the SDK we patched.
  assert.equal(resolveCallProvider(resource(null), "anthropic"), "anthropic");
});

test("gateway-routed calls report the upstream, not the gateway", () => {
  // With routing on, every client points at the gateway. Resolving from that URL would
  // report the gateway's own host for every provider, so the upstream is recovered from
  // the gateway PATH instead.
  enableGateway("agent-key", "https://api.pyyol.com");
  try {
    const resource = { _client: { baseURL: "https://api.pyyol.com/gw/anthropic" } };
    assert.equal(resolveCallProvider(resource, "openai"), "anthropic");
  } finally {
    disableGateway();
  }
});
