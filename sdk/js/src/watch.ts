/**
 * The watch prompt: where a developer chooses to follow a match.
 *
 * Mirrors `pyyol/console.py::ask_watch` in the Python SDK. The two must behave
 * identically — a developer who switches language should not discover that one of them
 * hangs their CI and the other does not.
 *
 * A match used to open a browser tab on its own the moment it started. That is the wrong
 * default in both directions: on a remote box or in tmux the tab goes nowhere, and a
 * developer who ran a command in a terminal did not necessarily ask to have their screen
 * taken over. So we ask, once, and remember.
 */

import { createInterface } from "node:readline";

export const WATCH_BROWSER = "browser";
export const WATCH_TERMINAL = "terminal";
export type WatchChoice = typeof WATCH_BROWSER | typeof WATCH_TERMINAL;

export interface AskWatchOpts {
  /** Seconds before the default is taken. Must not outlive the start countdown. */
  timeoutMs?: number;
  stdin?: NodeJS.ReadableStream & { isTTY?: boolean };
  stdout?: NodeJS.WritableStream & { isTTY?: boolean };
  color?: boolean;
}

// Stripping ANSI escapes is how a row's true on-screen width is measured.
// eslint-disable-next-line no-control-regex -- matching them is the point
const ANSI = /\x1b\[[0-9;]*m/g;
const visibleLen = (s: string): number => s.replace(ANSI, "").length;

export const DEFAULT_DASHBOARD =
  (process.env.PYYOL_DASHBOARD || "").replace(/\/$/, "") || "https://pyyol.com";

/**
 * Where a running match is watched in the browser, per game. Verified against the
 * client's routes: Goofspiel and Monopoly take ?match= at the top level; Mafia's viewer
 * lives under /arena. A wrong path is worse than no link — it lands the developer on a
 * DIFFERENT live match and everything they see is someone else's game.
 *
 * Lives here rather than in cli.ts because the runtime needs it too, and the runtime
 * cannot import the CLI (the CLI imports the runtime). One copy, so the two paths cannot
 * drift into disagreeing about where a match is watched.
 */
const WATCH_ROUTE: Record<string, string> = {
  goofspiel: "/goofspiel",
  mafia: "/arena/mafia",
};

/** Browser URL for a specific live match, or "" when it cannot be named exactly. */
export function watchUrl(arena: string, matchId: string, dashboard = DEFAULT_DASHBOARD): string {
  const route = WATCH_ROUTE[arena];
  if (!route || !matchId || !dashboard) return "";
  // encodeURIComponent (not encodeURI) so a slash is escaped too, and cannot alter the
  // path instead of the query.
  return `${dashboard.replace(/\/$/, "")}${route}?match=${encodeURIComponent(matchId)}`;
}

/**
 * Ask where to watch this match.
 *
 * Three rules keep this from becoming a liability:
 *
 * **It never blocks a machine.** No TTY on stdin OR stdout means nobody is there to
 * answer — CI, a pipe, a systemd unit — so it resolves to the terminal default without
 * printing a prompt at all. A prompt that can hang a pipeline is worse than no prompt.
 *
 * **It never outlives the countdown.** The read is bounded and defaults on expiry. The
 * match begins whether or not this question was answered, and a prompt still on screen
 * after play started is asking about a decision that is already gone.
 *
 * **It never keeps the process alive.** The readline interface is closed and stdin is
 * paused on every path, including the timeout. A lingering stdin listener would hold the
 * event loop open and a finished `pyyol play` would simply never exit.
 */
export async function askWatch(label: string, url: string, opts: AskWatchOpts = {}): Promise<WatchChoice> {
  const timeoutMs = opts.timeoutMs ?? 10_000;
  const stdin = opts.stdin ?? process.stdin;
  const stdout = opts.stdout ?? process.stdout;

  if (!stdin.isTTY || !stdout.isTTY) return WATCH_TERMINAL;

  const color = opts.color ?? process.env.NO_COLOR === undefined;
  const c = (text: string, code: string): string => (color ? `\x1b[${code}m${text}\x1b[0m` : text);

  const rows = [
    c(label, "36"),
    "",
    `${c("[b]", "1")} watch the live table in your browser`,
    `${c("[t]", "1")} follow the logs here          ${c("· default", "90")}`,
  ];
  // Sized to the content, measured on the UNCOLOURED text: an escape sequence takes
  // columns in a string and none on screen, so padding by raw length draws a box that is
  // crooked exactly when colour is on.
  const width = Math.max(...rows.map(visibleLen)) + 2;
  stdout.write("\n" + c("╭─ match found " + "─".repeat(Math.max(0, width - 13)) + "╮", "90") + "\n");
  for (const r of rows) {
    stdout.write(c("│", "90") + " " + r + " ".repeat(width - visibleLen(r)) + c("│", "90") + "\n");
  }
  stdout.write(c("╰" + "─".repeat(width + 1) + "╯", "90") + "\n");
  if (url) stdout.write("  " + c(url, "90") + "\n");
  stdout.write("  " + c("›", "36") + " ");

  const answer = await readLine(stdin, timeoutMs);
  if (answer === null) {
    // Say the default was taken. An unexplained newline reads as a dropped keystroke.
    stdout.write("\n  " + c(`no answer in ${Math.round(timeoutMs / 1000)}s — following here`, "90") + "\n");
    return WATCH_TERMINAL;
  }
  return answer.trim().toLowerCase().startsWith("b") ? WATCH_BROWSER : WATCH_TERMINAL;
}

/** One line from stdin, or null if the timeout wins. Always tears the reader down. */
function readLine(stdin: NodeJS.ReadableStream, timeoutMs: number): Promise<string | null> {
  return new Promise((resolve) => {
    const rl = createInterface({ input: stdin });
    let done = false;
    const finish = (v: string | null): void => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      rl.close();
      // pause(), or a resumed stdin keeps the event loop alive and the CLI never exits.
      if (typeof (stdin as NodeJS.ReadStream).pause === "function") (stdin as NodeJS.ReadStream).pause();
      resolve(v);
    };
    // Deliberately NOT unref'd. An unref'd timer may never fire, and this timer firing
    // is what RESOLVES the promise — unref turned "defaults after ten seconds" into
    // "hangs forever" whenever nothing else kept the event loop alive. It is short and
    // cleared on every answered path, so it cannot hold the process open for long.
    const timer = setTimeout(() => finish(null), timeoutMs);
    rl.once("line", (line: string) => finish(line));
    // A closed stdin is a non-answer, not a crash mid-match.
    rl.once("close", () => finish(null));
    rl.once("error", () => finish(null));
  });
}
