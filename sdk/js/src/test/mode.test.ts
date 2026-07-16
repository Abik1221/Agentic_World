import assert from "node:assert/strict";
import { test } from "node:test";

import * as mode from "../mode.js";

test("dev is always sandbox, even if ranked is requested", () => {
  process.env.PYYOL_MODE = "ranked";
  assert.equal(mode.resolveMode({ rankedFlag: true, cfgMode: "ranked", devLocked: true }), mode.SANDBOX);
  delete process.env.PYYOL_MODE;
});

test("precedence: flag > env > cfg > default", () => {
  delete process.env.PYYOL_MODE;
  assert.equal(mode.resolveMode({}), mode.SANDBOX);
  assert.equal(mode.resolveMode({ cfgMode: "ranked" }), mode.RANKED);
  process.env.PYYOL_MODE = "sandbox";
  assert.equal(mode.resolveMode({ cfgMode: "ranked" }), mode.SANDBOX); // env beats cfg
  delete process.env.PYYOL_MODE;
  assert.equal(mode.resolveMode({ rankedFlag: true, cfgMode: "sandbox" }), mode.RANKED); // flag wins
});

test("banner labels", () => {
  assert.match(mode.banner(mode.SANDBOX, false), /SANDBOX/);
  assert.match(mode.banner(mode.RANKED, false), /RANKED/);
});

test("confirmRanked honors assumeYes", async () => {
  assert.equal(await mode.confirmRanked(true), true);
});
