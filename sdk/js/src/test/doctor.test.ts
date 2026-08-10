// `pyyol doctor`'s verified-tier section, mirroring the Python SDK's.
//
// An agent can run perfectly and earn nothing: the platform ranks VERIFIED play, so one whose calls
// are never proven is invisible to the model board however well it plays. Nothing else in the
// toolchain says so, and learning it from an empty leaderboard row weeks later is the failure this
// prevents.
//
// The cross-SDK case at the bottom is the point of writing these at all: a developer must not get a
// different answer about their own eligibility depending on which SDK they installed.

import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { printVerifiedReadiness, scaffoldHint } from "../cli.js";
import { disableGateway, enableGateway } from "../instrument.js";
import { explain, ISSUE_NO_SYSTEM_PROMPT } from "../scaffold.js";

function agentFile(source: string): string {
  const dir = mkdtempSync(join(tmpdir(), "pyyol-doctor-"));
  const path = join(dir, "agent.ts");
  writeFileSync(path, source, "utf8");
  return path;
}

async function render(
  base = "",
  c: { accessToken?: string } | null = null,
  cfg: { entry?: string } | null = null,
): Promise<string> {
  const lines: string[] = [];
  const original = console.log;
  console.log = (...args: unknown[]) => void lines.push(args.map(String).join(" "));
  try {
    await printVerifiedReadiness(base, c, cfg);
  } finally {
    console.log = original;
  }
  return lines.join("\n");
}

test("an unrouted agent is told it cannot be proven", async () => {
  // The most important line in the command. Without routing there is nothing to prove, so the agent
  // never reaches the model board — and every other check will look fine.
  disableGateway();
  const out = await render();
  assert.ok(out.includes("gateway routing"));
  assert.ok(out.includes("pyyol.route"), "the fix must be named, not just the problem");
  assert.ok(out.includes("model board"));
});

test("a routed agent is told its calls are observed", async () => {
  enableGateway("sk_arena_x", "https://api.pyyol.test/v1");
  try {
    assert.ok((await render()).includes("server-observed"));
  } finally {
    disableGateway();
  }
});

test("an agent with no system prompt is told why it is excluded", async () => {
  // Instructions living in the user turn cannot be told apart from the game state, so the harness
  // cannot be fingerprinted and the agent is excluded from paired model comparison. The message has
  // to name the fix, not merely report that something is wrong.
  const entry = agentFile(
    `await client.chat.completions.create({ model: "gpt-4o",\n` +
      `  messages: [{ role: "user", content: "play well " + JSON.stringify(view) }] });\n`,
  );
  assert.equal(await scaffoldHint(entry), false);
  const out = await render("", null, { entry });
  assert.ok(out.includes("system prompt"));
  assert.ok(out.includes("system message"), "the message must name the fix");
});

test("an agent with a system prompt is told it qualifies", async () => {
  const entry = agentFile(
    `const SYSTEM = "You play Goofspiel.";\n` +
      `await client.messages.create({ system: SYSTEM, messages: [{ role: "user", content: "x" }] });\n`,
  );
  assert.equal(await scaffoldHint(entry), true);
  assert.ok((await render("", null, { entry })).includes("eligible"));
});

test("an unreadable entry says unknown rather than accusing", async () => {
  // "We could not tell" and "you did it wrong" are different claims, and guessing the second sends a
  // developer to fix something that is not broken.
  const entry = join(tmpdir(), "pyyol-doctor-missing", "nope.ts");
  assert.equal(await scaffoldHint(entry), null);
  const out = await render("", null, { entry });
  assert.ok(out.includes("could not inspect"));
  assert.ok(out.includes("trace"), "an unknown answer must point at the authoritative one");
});

test("no config is unknown, not a failure", async () => {
  assert.equal(await scaffoldHint(undefined), null);
});

test("the OpenAI developer role counts as a system prompt", async () => {
  // "developer" is OpenAI's newer name for the system role. Missing it would tell a correctly built
  // agent it is ineligible, which is worse than saying nothing.
  const entry = agentFile(`const msgs = [{ role: "developer", content: "You play Mafia." }];\n`);
  assert.equal(await scaffoldHint(entry), true);
});

test("both SDKs give a developer the same sentence about the same rule", async () => {
  // The reason these tests exist. The Python doctor prints scaffold.explain(ISSUE_NO_SYSTEM_PROMPT)
  // and so does this one; the string itself is pinned by the shared conformance fixtures. If the two
  // ever paraphrase independently, a developer switching SDKs gets two different explanations of one
  // rule and stops believing either.
  const prose = explain(ISSUE_NO_SYSTEM_PROMPT);
  assert.ok(prose.includes("system message"));
  const entry = agentFile(`messages: [{ role: "user", content: "play" }]\n`);
  assert.ok((await render("", null, { entry })).includes(prose), "the doctor must not paraphrase");
});
