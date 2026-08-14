/**
 * The CLI must never show a developer our internals.
 *
 * The bin entry printed `e.message` and exited 1, so an internal fault read exactly like
 * something the DEVELOPER had done wrong and no script could tell a reported failure from a
 * broken tool. These pin the replacement, and they mirror
 * sdk/python/tests/test_crash_boundary.py case for case — the two SDKs must behave identically.
 */

import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { EXIT_INTERNAL, EXIT_INTERRUPTED, reportCrash } from "../crash.js";

/** Capture stderr for one call. */
function captureErr(fn: () => void): string {
  const lines: string[] = [];
  const original = console.error;
  console.error = (...args: unknown[]) => {
    lines.push(args.map(String).join(" "));
  };
  try {
    fn();
  } finally {
    console.error = original;
  }
  return lines.join("\n");
}

function withStateDir(fn: (dir: string) => void): void {
  const dir = mkdtempSync(join(tmpdir(), "pyyol-crash-"));
  const prevState = process.env.XDG_STATE_HOME;
  const prevDebug = process.env.PYYOL_DEBUG;
  process.env.XDG_STATE_HOME = dir;
  delete process.env.PYYOL_DEBUG;
  try {
    fn(dir);
  } finally {
    if (prevState === undefined) delete process.env.XDG_STATE_HOME;
    else process.env.XDG_STATE_HOME = prevState;
    if (prevDebug !== undefined) process.env.PYYOL_DEBUG = prevDebug;
  }
}

test("an internal fault exits 70 and says whose bug it is", () => {
  withStateDir((dir) => {
    let code = 0;
    const err = captureErr(() => {
      code = reportCrash(new TypeError("Cannot read properties of undefined"), "whoami", "9.9.9");
    });
    assert.equal(code, EXIT_INTERNAL, "an internal fault must be distinguishable from a reported failure");
    // The single most important line: without it a developer hunts through their own agent
    // for a fault that is ours.
    assert.match(err, /bug in pyyol, not in your agent/);
    // The TYPE is kept: "TypeError: x" says more than "x" alone, and a bare message reads
    // like the developer's own mistake.
    assert.match(err, /TypeError: Cannot read properties of undefined/);
    assert.match(err, /PYYOL_DEBUG=1/, "the developer must be told how to get the full detail");
    const report = join(dir, "pyyol", "last-crash.log");
    assert.ok(existsSync(report), "a crash report must be written");
    const body = readFileSync(report, "utf8");
    assert.match(body, /9\.9\.9/);
  });
});

test("the crash report records the command but not its arguments", () => {
  // A crash file is something a developer may paste into a public issue, and pyyol's own
  // arguments include agent names and, on some commands, tokens.
  withStateDir((dir) => {
    captureErr(() => reportCrash(new Error("x"), "publish", "1"));
    const body = readFileSync(join(dir, "pyyol", "last-crash.log"), "utf8");
    assert.match(body, /command: pyyol publish/);
    assert.ok(!body.includes("--token"), "a crash report must never carry argument values");
  });
});

test("a non-Error throw is still reported rather than crashing the reporter", () => {
  // `throw "string"` and `Promise.reject(undefined)` both happen in the wild, including from
  // dependencies. The reporter must survive them: a crash inside the crash handler is the one
  // failure with no fallback left.
  withStateDir(() => {
    let code = 0;
    const err = captureErr(() => {
      code = reportCrash("plain string failure", "dev", "1");
    });
    assert.equal(code, EXIT_INTERNAL);
    assert.match(err, /plain string failure/);
  });
});

test("the exit codes follow the shell convention", () => {
  // 70 is EX_SOFTWARE and 130 is 128 + SIGINT. A tool that exits 0 or 1 on Ctrl-C makes `&&`
  // chains continue after a human has explicitly stopped them.
  assert.equal(EXIT_INTERNAL, 70);
  assert.equal(EXIT_INTERRUPTED, 130);
});
