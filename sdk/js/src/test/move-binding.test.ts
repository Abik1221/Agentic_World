// Cross-language conformance for COMPLETION BINDING, driven by shared fixtures.
//
// The expectations live in sdk/conformance/move_binding.json and are read by this suite, the
// Python SDK's test_move_binding.py and the Go gateway's internal/movebind/conformance_test.go.
//
// The point is drift. Three implementations of this reduction exist and nothing forces them to
// agree. And a divergence here does not surface as a visible bug — it surfaces as an HONEST
// TURN BEING REJECTED, because the gateway reduced the model's answer one way and the match
// compared it against the same move reduced another way.

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

import {
  boundMove,
  boundPlan,
  canonMafia,
  moveTool,
  moveToolChoice,
  moveToolName,
  NO_TARGET,
  GAME_GOOFSPIEL,
  GAME_MAFIA,
} from "../movetools.js";

interface BindingCase {
  name: string;
  why: string;
  game: string;
  tool: string;
  response: unknown;
  // null means NOTHING may be bound. Those cases matter most: binding a wrong move rejects
  // honest play, while binding nothing merely leaves a turn unverified.
  expect_move: string | null;
}

const here = dirname(fileURLToPath(import.meta.url));
const fixturePath = resolve(here, "..", "..", "..", "conformance", "move_binding.json");

function load(): BindingCase[] {
  // A missing fixture file must fail loudly rather than silently pass: a conformance suite that
  // finds nothing and reports success is exactly the drift it exists to catch.
  let raw: string;
  try {
    raw = readFileSync(fixturePath, "utf8");
  } catch (e) {
    throw new Error(
      `move-binding fixtures not found at ${fixturePath}: ${String(e)}\n` +
        "These fixtures are shared with the Python SDK and the Go gateway and live in the " +
        "sibling sdk/conformance directory. Run tests from a full checkout of the repository.",
    );
  }
  const doc = JSON.parse(raw) as { cases: BindingCase[]; plan_cases: PlanCase[] };
  assert.ok(doc.cases?.length, "move_binding.json contains no cases");
  assert.ok(doc.plan_cases?.length, "move_binding.json contains no plan_cases");
  planCases = doc.plan_cases;
  return doc.cases;
}

// The RANGE half of the contract: one completion that decided several rounds.
//
// Shared for the same reason as the single-move cases. The gateway writes a bound row per round
// in the span and the match checks each one as it is submitted, so an SDK that builds a plan the
// gateway reduces differently rejects an honest turn in the MIDDLE of a batched sequence — the
// hardest possible failure to debug from either side.
interface PlanCase {
  name: string;
  why: string;
  game: string;
  tool: string;
  proven_round: number;
  response: unknown;
  // null means NOTHING may be bound, the same distinction expect_move draws.
  expect_plan: { round: number; move: string }[] | null;
}

let planCases: PlanCase[] = [];

for (const c of load()) {
  test(`move binding conformance: ${c.name}`, () => {
    // The fixture names the tool AND the game, so this also pins that the two agree. A game
    // whose tool name disagreed would mean the SDK sends a tool the gateway does not look for,
    // and every move would silently go unbound.
    assert.equal(
      moveToolName(c.game),
      c.tool,
      `moveToolName(${c.game}) disagrees with the fixture's tool name`,
    );
    const got = boundMove(c.game, c.response);
    if (c.expect_move === null) {
      assert.equal(got, null, `bound ${JSON.stringify(got)} but must bind NOTHING.\nwhy: ${c.why}`);
      return;
    }
    assert.equal(got, c.expect_move, `why: ${c.why}`);
  });
}

test("tool definitions carry the right name and the same schema per provider", () => {
  for (const game of [GAME_GOOFSPIEL, GAME_MAFIA]) {
    const want = moveToolName(game);
    const openai = moveTool(game, "openai") as {
      type: string;
      function: { name: string; parameters: unknown };
    };
    assert.equal(openai.type, "function");
    assert.equal(openai.function.name, want);
    assert.equal((moveTool(game, "anthropic") as { name: string }).name, want);
    assert.equal((moveTool(game, "google") as { name: string }).name, want);
    // The JSON Schema must be identical in every envelope, or a model told one thing by one
    // provider and another by the next would emit inconsistent arguments.
    assert.deepEqual(
      openai.function.parameters,
      (moveTool(game, "anthropic") as { input_schema: unknown }).input_schema,
    );
    assert.deepEqual(
      openai.function.parameters,
      (moveTool(game, "google") as { parameters: unknown }).parameters,
    );
  }
});

test("tool choice requires the move tool", () => {
  // toolChoice is what turns "the model may call this" into "the model must", and a turn with
  // no tool call earns no binding at all.
  assert.deepEqual(moveToolChoice("goofspiel", "anthropic"), {
    type: "tool",
    name: "play_card",
  });
  assert.deepEqual(moveToolChoice("goofspiel", "openai"), {
    type: "function",
    function: { name: "play_card" },
  });
  const google = moveToolChoice("goofspiel", "google") as {
    function_calling_config: { allowed_function_names: string[] };
  };
  assert.deepEqual(google.function_calling_config.allowed_function_names, ["play_card"]);
});

test("an unknown game has no tool and throws rather than guessing", () => {
  assert.equal(moveToolName("chess"), "");
  assert.throws(() => moveTool("chess"));
});

test("no target is -1, not seat 0", () => {
  // The one constant worth asserting directly: seat 0 is a real player. If NO_TARGET were 0,
  // every untargeted action would bind as an action against that player, and the honest
  // untargeted move that followed would be rejected.
  assert.equal(NO_TARGET, -1);
  assert.equal(canonMafia("vote", -1), "vote:none");
  assert.equal(canonMafia("vote", 0), "vote:0");
  assert.notEqual(canonMafia("vote", -1), canonMafia("vote", 0));
});

test("a quoted integer binds but an exponent or decimal string does not", () => {
  // "7" is the same decision written differently. "1e3" and "7.0" are not integers a model
  // wrote as a card, and accepting them would bind a value the model did not name — which
  // would then reject its real move. Pinned because Number() would accept both.
  assert.equal(boundMove("goofspiel", { content: [tool({ card: "7" })] }), "card:7");
  assert.equal(boundMove("goofspiel", { content: [tool({ card: "1e3" })] }), null);
  assert.equal(boundMove("goofspiel", { content: [tool({ card: "7.0" })] }), null);
  assert.equal(boundMove("goofspiel", { content: [tool({ card: "" })] }), null);
});

function tool(input: Record<string, unknown>): Record<string, unknown> {
  return { type: "tool_use", name: "play_card", input };
}

for (const c of planCases) {
  test(`move binding plan conformance: ${c.name}`, () => {
    assert.equal(
      moveToolName(c.game),
      c.tool,
      `moveToolName(${c.game}) disagrees with the fixture's tool name`,
    );
    const got = boundPlan(c.game, c.response, c.proven_round);
    if (c.expect_plan === null) {
      assert.equal(got, null, `bound ${JSON.stringify(got)} but must bind NOTHING.\nwhy: ${c.why}`);
      return;
    }
    assert.ok(got, `bound nothing, want ${c.expect_plan.length} rounds.\nwhy: ${c.why}`);
    assert.deepEqual(
      got,
      c.expect_plan,
      `bound ${JSON.stringify(got)}, want ${JSON.stringify(c.expect_plan)}.\nwhy: ${c.why}`,
    );
  });
}
