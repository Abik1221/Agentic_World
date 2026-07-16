import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import * as config from "../config.js";

test("save/load roundtrip preserves fields + safe default mode", () => {
  const dir = mkdtempSync(join(tmpdir(), "pyyol-"));
  const cfg = { ...config.defaults(), name: "atlas", framework: "langgraph", arena: "mafia" };
  const path = config.save(cfg, dir);
  const loaded = config.load(path);
  assert.ok(loaded);
  assert.equal(loaded!.name, "atlas");
  assert.equal(loaded!.framework, "langgraph");
  assert.equal(loaded!.arena, "mafia");
  assert.equal(loaded!.mode, "sandbox"); // safe default persisted
});

test("agent_id omitted until set, then persisted (idempotent)", () => {
  const dir = mkdtempSync(join(tmpdir(), "pyyol-"));
  const path = config.save({ ...config.defaults(), name: "atlas" }, dir);
  assert.ok(!config.load(path)!.agent_id);
  assert.equal(config.setAgentId("agt_abc", path), true);
  assert.equal(config.load(path)!.agent_id, "agt_abc");
  assert.equal(config.setAgentId("agt_abc", path), false); // no rewrite needed
});

test("entryParts defaults the variable", () => {
  assert.deepEqual(config.entryParts({ ...config.defaults(), entry: "agent.mjs" }), ["agent.mjs", "agent"]);
  assert.deepEqual(config.entryParts({ ...config.defaults(), entry: "src/bot.mjs:brain" }), ["src/bot.mjs", "brain"]);
});

test("validate flags bad mode + arena", () => {
  const problems = config.validate({ ...config.defaults(), name: "x", mode: "live", arena: "chess" });
  assert.ok(problems.some((p) => p.includes("mode")));
  assert.ok(problems.some((p) => p.includes("arena")));
  assert.deepEqual(config.validate({ ...config.defaults(), name: "ok" }), []);
});

test("infer detects a JS project", () => {
  const dir = mkdtempSync(join(tmpdir(), "pyyol-"));
  writeFileSync(join(dir, "agent.mjs"), "export const agent = {};\n");
  const cfg = config.infer(dir);
  assert.equal(cfg.language, "javascript");
  assert.equal(cfg.entry, "agent.mjs:agent");
});
