import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync } from "node:fs";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { maybeInstallPing } from "../install-ping.js";

// Run maybeInstallPing with an isolated PYYOL_HOME, a stubbed fetch, and controlled
// opt-out env; everything is restored afterward.
function withEnv(
  opts: { home: string; optOut?: string },
  fn: (calls: { url: string; body: any }[]) => void,
): void {
  const origHome = process.env.PYYOL_HOME;
  const origNo = process.env.PYYOL_NO_TELEMETRY;
  const origDnt = process.env.DO_NOT_TRACK;
  const origFetch = globalThis.fetch;
  const calls: { url: string; body: any }[] = [];
  process.env.PYYOL_HOME = opts.home;
  delete process.env.PYYOL_NO_TELEMETRY;
  delete process.env.DO_NOT_TRACK;
  if (opts.optOut) process.env[opts.optOut] = "1";
  globalThis.fetch = (async (url: string, init?: RequestInit) => {
    calls.push({ url: String(url), body: JSON.parse(String(init?.body ?? "{}")) });
    return new Response("", { status: 204 });
  }) as unknown as typeof fetch;
  try {
    fn(calls);
  } finally {
    globalThis.fetch = origFetch;
    if (origHome === undefined) delete process.env.PYYOL_HOME;
    else process.env.PYYOL_HOME = origHome;
    if (origNo === undefined) delete process.env.PYYOL_NO_TELEMETRY;
    else process.env.PYYOL_NO_TELEMETRY = origNo;
    if (origDnt === undefined) delete process.env.DO_NOT_TRACK;
    else process.env.DO_NOT_TRACK = origDnt;
  }
}

test("fires once with {sdk:js, version} + writes a marker; dedups", () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-ping-"));
  withEnv({ home }, (calls) => {
    maybeInstallPing("https://api.pyyol.com", "9.9.9");
    assert.equal(calls.length, 1);
    assert.match(calls[0].url, /\/v1\/telemetry\/install$/);
    assert.deepEqual(calls[0].body, { sdk: "js", version: "9.9.9" });
    assert.ok(existsSync(join(home, ".install_pinged_9.9.9")));
    // Second call same version → deduped.
    maybeInstallPing("https://api.pyyol.com", "9.9.9");
    assert.equal(calls.length, 1);
  });
});

test("opt-out env suppresses the ping", () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-ping2-"));
  withEnv({ home, optOut: "DO_NOT_TRACK" }, (calls) => {
    maybeInstallPing("https://api.pyyol.com", "9.9.9");
    assert.equal(calls.length, 0);
  });
});

test("no api base is a no-op", () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-ping3-"));
  withEnv({ home }, (calls) => {
    maybeInstallPing("", "9.9.9");
    assert.equal(calls.length, 0);
  });
});
