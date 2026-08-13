/**
 * The watch prompt. Mirrors `sdk/python/tests/test_watch_prompt.py` case for case —
 * the two SDKs must behave identically, and the failure that matters here is not a
 * wrong colour but a prompt that blocks a pipeline or holds the process open.
 */

import assert from "node:assert/strict";
import { PassThrough, Writable } from "node:stream";
import { test } from "node:test";

import { askWatch, WATCH_BROWSER, WATCH_TERMINAL } from "../watch.js";

interface Sink extends Writable {
  isTTY?: boolean;
  text(): string;
}

function sink(tty = true): Sink {
  const chunks: string[] = [];
  const w = new Writable({
    write(c, _e, cb) {
      chunks.push(String(c));
      cb();
    },
  }) as Sink;
  w.isTTY = tty;
  w.text = () => chunks.join("");
  return w;
}

function source(data: string | null, tty = true): PassThrough & { isTTY?: boolean } {
  const s = new PassThrough() as PassThrough & { isTTY?: boolean };
  s.isTTY = tty;
  if (data !== null) s.write(data);
  return s;
}

// Stripping ANSI escapes is how a row's true on-screen width is measured.
// eslint-disable-next-line no-control-regex -- matching them is the point
const visible = (s: string): number => s.replace(/\x1b\[[0-9;]*m/g, "").length;

test("no TTY returns terminal without prompting", async () => {
  // The test that keeps CI alive.
  const out = sink(false);
  const got = await askWatch("g · m", "http://x", { stdin: source("b\n", false), stdout: out });
  assert.equal(got, WATCH_TERMINAL);
  assert.equal(out.text(), "", "a non-interactive run must print no prompt at all");
});

test("stdout is a TTY but stdin is a pipe — still skipped", async () => {
  // `pyyol play | tee log`: nobody can answer it, so asking would hang the pipeline.
  const out = sink(true);
  const got = await askWatch("g · m", "http://x", { stdin: source("b\n", false), stdout: out });
  assert.equal(got, WATCH_TERMINAL);
  assert.equal(out.text(), "");
});

test("b chooses the browser", async () => {
  const got = await askWatch("g · m", "http://x", { stdin: source("b\n"), stdout: sink() });
  assert.equal(got, WATCH_BROWSER);
});

test("everything other than b follows here", async () => {
  // The default must be the SAFE one: staying put cannot fail, opening a tab can.
  for (const answer of ["t\n", "\n", "anything\n"]) {
    const got = await askWatch("g · m", "http://x", { stdin: source(answer), stdout: sink() });
    assert.equal(got, WATCH_TERMINAL, `answer ${JSON.stringify(answer)}`);
  }
});

test("silence defaults within the timeout", async () => {
  // A developer who walked away must not hold the match open, and the wait must end AT
  // the timeout — the match starts on the server's schedule regardless.
  const out = sink();
  const t0 = Date.now();
  const got = await askWatch("g · m", "http://x", {
    stdin: source(null),
    stdout: out,
    timeoutMs: 300,
  });
  const elapsed = Date.now() - t0;
  assert.equal(got, WATCH_TERMINAL);
  assert.ok(elapsed < 3000, `waited ${elapsed}ms on a 300ms timeout — it outlived the countdown`);
  assert.ok(out.text().includes("no answer"), "a silent default reads as a dropped keystroke");
});

test("the box is aligned regardless of match id length", async () => {
  // Padding is computed on the UNCOLOURED text: an escape sequence takes columns in a
  // string and none in a terminal, so measuring the coloured row draws a crooked box
  // exactly when colour is on.
  const out = sink();
  await askWatch("goofspiel · m_tqp7ze5jzmn7xoxu_and_then_some", "http://x", {
    stdin: source(null),
    stdout: out,
    timeoutMs: 100,
    color: true,
  });
  // eslint-disable-next-line no-control-regex -- the border rows are literally ANSI-prefixed
  const box = out.text().split("\n").filter((l) => /^\x1b\[90m[╭│╰]/.test(l));
  assert.ok(box.length >= 3, `found ${box.length} border rows`);
  const widths = new Set(box.map(visible));
  assert.equal(widths.size, 1, `border rows disagree on width: ${[...widths].join(", ")}`);
});

test("colour off emits no escape codes", async () => {
  const out = sink();
  await askWatch("g · m", "http://x", {
    stdin: source(null),
    stdout: out,
    timeoutMs: 100,
    color: false,
  });
  assert.ok(!out.text().includes("\x1b["), "literal escape bytes on a dumb terminal");
});
