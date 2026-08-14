/**
 * The last line of defence between a bug in this CLI and the developer using it.
 *
 * # What a developer used to see
 *
 * The bin entry ended in `.catch((e) => { console.error(`✗ ${e.message}`); process.exitCode = 1; })`.
 * Better than a stack trace, and still wrong in three ways:
 *
 *  - An internal fault printed as `✗ Cannot read properties of undefined (reading 'name')`,
 *    which reads exactly like something the DEVELOPER did wrong. They go hunting through their
 *    own agent for a fault that is ours.
 *  - Every failure exited 1, so a script could not tell "the command ran and told you it
 *    failed" from "the command itself broke".
 *  - The detail that would let us fix it — the stack, the version, the platform — was thrown
 *    away at the moment it was most needed.
 *
 * # What replaces it
 *
 * The same contract the Python SDK uses, because the two must behave identically: a short
 * honest report that says this is our bug, a crash file with the full stack, an env var to
 * print it inline, and exit codes a pipeline can branch on.
 *
 *     0    fine
 *     1    an ordinary, expected failure (already reported by the command itself)
 *     2    usage error
 *     70   an internal fault — this module's job (EX_SOFTWARE, sysexits.h)
 *     130  interrupted with Ctrl-C (128 + SIGINT)
 */

import { mkdirSync, writeFileSync } from "node:fs";
import { homedir, tmpdir, platform } from "node:os";
import { join } from "node:path";

/** EX_SOFTWARE from sysexits.h — "the command itself broke", distinct from a reported failure. */
export const EXIT_INTERNAL = 70;
/** 128 + SIGINT. A tool that exits 0 on Ctrl-C makes `&&` chains continue after a human
 *  explicitly stopped them. */
export const EXIT_INTERRUPTED = 130;

const ISSUES_URL = "https://github.com/pyyol/pyyol/issues/new";

/**
 * Where crash reports go. XDG_STATE_HOME is the correct home for this (state that is neither
 * config nor cache), falling back to ~/.local/state and then the temp dir — a read-only or
 * unusual HOME must not turn a crash report into a second crash.
 */
function crashDir(): string {
  const base = process.env.XDG_STATE_HOME || join(homedir(), ".local", "state");
  try {
    const dir = join(base, "pyyol");
    mkdirSync(dir, { recursive: true });
    return dir;
  } catch {
    return tmpdir();
  }
}

/**
 * Write the full stack somewhere retrievable. Returns null if it cannot — failing to write a
 * crash report must never replace the crash message with a different error.
 */
function writeReport(err: unknown, command: string, version: string): string | null {
  try {
    const path = join(crashDir(), "last-crash.log");
    const stack = err instanceof Error ? (err.stack ?? err.message) : String(err);
    const body =
      `pyyol ${version}\n` +
      `node ${process.versions.node} on ${platform()}\n` +
      // The command only — never the arguments. This is a file a developer may paste into a
      // public issue, and pyyol's arguments include agent names and, on some commands, tokens.
      `command: pyyol ${command}\n\n${stack}\n`;
    writeFileSync(path, body, "utf8");
    return path;
  } catch {
    return null;
  }
}

/** The error on one line, keeping its TYPE — "TypeError: x" says more than "x" alone. */
function oneLine(err: unknown): string {
  if (!(err instanceof Error)) return String(err).split("\n")[0] ?? "unknown error";
  const head = (err.message || "").split("\n")[0] ?? "";
  const name = err.name || "Error";
  if (!head) return name;
  const trimmed = head.length > 160 ? `${head.slice(0, 157)}…` : head;
  return `${name}: ${trimmed}`;
}

/**
 * Report an internal fault and return the exit code to use.
 *
 * Separate from the runner so the bin entry can use it for BOTH the rejected-promise path and
 * any synchronous throw, without either duplicating the wording.
 */
export function reportCrash(err: unknown, command: string, version: string): number {
  if (process.env.PYYOL_DEBUG) {
    // Printed BEFORE the summary so the summary stays the last thing on screen.
    console.error(err instanceof Error ? (err.stack ?? err.message) : String(err));
  }
  const report = writeReport(err, command, version);
  console.error("");
  console.error(`✗ pyyol hit an internal error while running \`${command}\`.`);
  console.error(`  ${oneLine(err)}`);
  console.error("");
  // Said plainly, because the default assumption is the opposite.
  console.error("  This is a bug in pyyol, not in your agent.");
  if (report) console.error(`  Full details: ${report}`);
  console.error(`  Report it: ${ISSUES_URL}`);
  if (!process.env.PYYOL_DEBUG) {
    console.error("  Re-run with PYYOL_DEBUG=1 to print the full stack here.");
  }
  if (version) {
    console.error(`  pyyol ${version} · node ${process.versions.node} · ${platform()}`);
  }
  return EXIT_INTERNAL;
}

/**
 * Install the Ctrl-C handler.
 *
 * Node's default SIGINT already exits 130, but only while nothing has taken over the signal —
 * and `pyyol` (the shell) and `pyyol dev` both do. An explicit handler makes the behaviour the
 * same everywhere: a newline so the shell prompt does not land mid-line after the ^C, one
 * word, and the conventional code.
 */
export function installInterruptHandler(): void {
  process.on("SIGINT", () => {
    console.error("");
    console.error("Stopped.");
    process.exit(EXIT_INTERRUPTED);
  });
}

/**
 * Silence EPIPE.
 *
 * `pyyol leaderboard | head` closes the pipe early. That is the pipeline working, not a
 * failure — but the write that loses the race surfaces as an unhandled EPIPE and Node prints a
 * stack trace about it at shutdown.
 */
export function ignoreBrokenPipe(): void {
  const quiet = (err: NodeJS.ErrnoException) => {
    if (err && err.code === "EPIPE") process.exit(EXIT_INTERRUPTED);
  };
  process.stdout.on("error", quiet);
  process.stderr.on("error", quiet);
}
