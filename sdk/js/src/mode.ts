/**
 * Money-safety mode model (mirrors the Python SDK): `sandbox` is the safe default;
 * `ranked` (real stakes) needs an explicit opt-in. `pyyol dev` is hard-locked to
 * sandbox; only `pyyol play <arena> --ranked` can reach ranked. Precedence for
 * `play`: --ranked flag → PYYOL_MODE env → pyyol.toml → default sandbox.
 */
import { createInterface } from "node:readline";

export const SANDBOX = "sandbox";
export const RANKED = "ranked";

const GREEN = "\x1b[32m";
const RED = "\x1b[1;31m";
const RESET = "\x1b[0m";

function useColor(): boolean {
  if (process.env.NO_COLOR) return false;
  return Boolean(process.stdout.isTTY);
}

export function resolveMode(opts: {
  rankedFlag?: boolean;
  cfgMode?: string;
  devLocked?: boolean;
}): string {
  if (opts.devLocked) return SANDBOX; // development never risks money
  if (opts.rankedFlag) return RANKED;
  const env = (process.env.PYYOL_MODE ?? "").trim().toLowerCase();
  if (env === SANDBOX || env === RANKED) return env;
  if (opts.cfgMode === SANDBOX || opts.cfgMode === RANKED) return opts.cfgMode;
  return SANDBOX;
}

export function banner(mode: string, color?: boolean): string {
  const use = color === undefined ? useColor() : color;
  if (mode === RANKED) {
    const text = "⚠  RANKED — real stakes (escrow · Elo · P-Index)";
    return use ? `${RED}${text}${RESET}` : text;
  }
  const text = "●  SANDBOX — practice, no stakes";
  return use ? `${GREEN}${text}${RESET}` : text;
}

/** One-time confirmation before real-stakes play. In CI/non-TTY, only proceeds when
 *  `assumeYes` is set — never silently enters ranked. */
export async function confirmRanked(assumeYes = false): Promise<boolean> {
  if (assumeYes) return true;
  if (!process.stdin.isTTY) return false;
  const rl = createInterface({ input: process.stdin, output: process.stdout });
  try {
    const answer: string = await new Promise((res) =>
      rl.question("This plays with REAL stakes. Continue? [y/N] ", res),
    );
    return ["y", "yes"].includes(answer.trim().toLowerCase());
  } finally {
    rl.close();
  }
}
