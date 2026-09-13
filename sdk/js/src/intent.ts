/**
 * Join vs Invite for `pyyol play` — mirrors `pyyol/console.py::ask_intent`.
 *
 * Same three rules as askWatch: never blocks a machine, never outlives the
 * countdown, never eats the agent's turn. Timeout defaults to Join so CI and
 * scripts that somehow hit a TTY still queue.
 */

import { createInterface } from "node:readline";
import { DEFAULT_DASHBOARD } from "./watch.js";

export const INTENT_QUEUE = "queue";
export const INTENT_INVITE = "invite";
export type StartupIntent = typeof INTENT_QUEUE | typeof INTENT_INVITE;

export interface AskIntentOpts {
  timeoutMs?: number;
  stdin?: NodeJS.ReadableStream & { isTTY?: boolean };
  stdout?: NodeJS.WritableStream & { isTTY?: boolean };
  color?: boolean;
}

// eslint-disable-next-line no-control-regex -- matching them is the point
const ANSI = /\x1b\[[0-9;]*m/g;
const visibleLen = (s: string): number => s.replace(ANSI, "").length;

/** Absolute Play-a-friend URL. Empty dashboard → no invented public link. */
export function friendsUrl(dashboard: string | null | undefined = DEFAULT_DASHBOARD): string {
  const base = (dashboard ?? "").replace(/\/$/, "");
  return base ? `${base}/friends` : "";
}

export async function askIntent(opts: AskIntentOpts = {}): Promise<StartupIntent> {
  const timeoutMs = opts.timeoutMs ?? 10_000;
  const stdin = opts.stdin ?? process.stdin;
  const stdout = opts.stdout ?? process.stdout;

  if (!stdin.isTTY || !stdout.isTTY) return INTENT_QUEUE;

  const color = opts.color ?? process.env.NO_COLOR === undefined;
  const c = (text: string, code: string): string => (color ? `\x1b[${code}m${text}\x1b[0m` : text);

  const rows = [
    c("how should this agent play?", "36"),
    "",
    `${c("[j]", "1")} Join a game                   ${c("· default", "90")}`,
    `${c("[i]", "1")} Invite a friend`,
  ];
  const width = Math.max(...rows.map(visibleLen)) + 2;
  stdout.write("\n" + c("╭─ pyyol play " + "─".repeat(Math.max(0, width - 12)) + "╮", "90") + "\n");
  for (const r of rows) {
    stdout.write(c("│", "90") + " " + r + " ".repeat(width - visibleLen(r)) + c("│", "90") + "\n");
  }
  stdout.write(c("╰" + "─".repeat(width + 1) + "╯", "90") + "\n");
  stdout.write("  " + c("›", "36") + " ");

  const answer = await readLine(stdin, timeoutMs);
  if (answer === null) {
    stdout.write("\n  " + c(`no answer in ${Math.round(timeoutMs / 1000)}s — joining a game`, "90") + "\n");
    return INTENT_QUEUE;
  }
  return answer.trim().toLowerCase().startsWith("i") ? INTENT_INVITE : INTENT_QUEUE;
}

export interface ResolveStartupIntentOpts {
  ranked?: boolean;
  mode?: string | null;
  queueFlag?: boolean;
  inviteFlag?: boolean;
  env?: Record<string, string | undefined>;
  isTty?: boolean;
  ask?: (opts?: AskIntentOpts) => Promise<StartupIntent>;
  askTimeoutMs?: number;
}

/** Decide queue vs invite for `pyyol play` (not `dev` / `run`). */
export async function resolveStartupIntent(opts: ResolveStartupIntentOpts = {}): Promise<StartupIntent> {
  if (opts.ranked || opts.queueFlag) return INTENT_QUEUE;
  if (opts.inviteFlag) return INTENT_INVITE;
  const m = (opts.mode || "").trim().toLowerCase();
  if (m === "queue") return INTENT_QUEUE;
  if (m === "invite") return INTENT_INVITE;
  const envMap = opts.env ?? process.env;
  const envV = (envMap.PYYOL_STARTUP || "").trim().toLowerCase();
  if (envV === INTENT_QUEUE || envV === INTENT_INVITE) return envV as StartupIntent;
  if (!opts.isTty) return INTENT_QUEUE;
  const asker = opts.ask ?? askIntent;
  return asker({ timeoutMs: opts.askTimeoutMs });
}

function readLine(stdin: NodeJS.ReadableStream, timeoutMs: number): Promise<string | null> {
  return new Promise((resolve) => {
    const rl = createInterface({ input: stdin });
    let done = false;
    const finish = (v: string | null): void => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      rl.close();
      if (typeof (stdin as NodeJS.ReadStream).pause === "function") (stdin as NodeJS.ReadStream).pause();
      resolve(v);
    };
    const timer = setTimeout(() => finish(null), timeoutMs);
    rl.once("line", (line: string) => finish(line));
    rl.once("close", () => finish(null));
    rl.once("error", () => finish(null));
  });
}
