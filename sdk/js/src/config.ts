/**
 * `pyyol.toml` — convention-over-configuration project config (mirrors the Python
 * SDK). Replaces the old manifest for onboarding. The schema is intentionally flat,
 * so we read/write it with a tiny built-in parser (no TOML dependency).
 *
 *     name = "atlas"
 *     language = "javascript"
 *     framework = "langgraph"
 *     arena = "goofspiel"
 *     visibility = "private"
 *     mode = "sandbox"          # sandbox (safe, default) | ranked (real stakes)
 *     entry = "agent.mjs:agent" # module:variable
 *     agent_id = "agt_…"        # written automatically after first registration
 */
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { basename, dirname, join, resolve } from "node:path";

export const CONFIG_NAME = "pyyol.toml";
export const KNOWN_ARENAS = ["goofspiel", "mafia", "monopoly"] as const;
export const MODES = ["sandbox", "ranked"] as const;

const ORDER = [
  "name",
  "language",
  "framework",
  "arena",
  "visibility",
  "mode",
  "entry",
  "agent_id",
] as const;

export interface Config {
  name: string;
  language: string;
  framework: string;
  arena: string;
  visibility: string;
  mode: string;
  entry: string;
  agent_id: string;
}

export function defaults(): Config {
  return {
    name: "",
    language: "javascript",
    framework: "",
    arena: "goofspiel",
    visibility: "private",
    mode: "sandbox",
    entry: "agent.mjs:agent",
    agent_id: "",
  };
}

/** Split `entry` into [modulePath, variable]; variable defaults to `agent`. */
export function entryParts(cfg: Config): [string, string] {
  const [mod, varName] = cfg.entry.split(":");
  return [mod || "agent.mjs", varName || "agent"];
}

/** Locate pyyol.toml from `start` (or cwd), walking up to the filesystem root. */
export function find(start = ""): string | null {
  let d = resolve(start || process.cwd());
  for (;;) {
    const candidate = join(d, CONFIG_NAME);
    if (existsSync(candidate)) return candidate;
    const parent = dirname(d);
    if (parent === d) return null;
    d = parent;
  }
}

/** Convention-first defaults for a directory (no file needed). */
export function infer(directory: string, name = ""): Config {
  const dir = resolve(directory);
  const cfg = defaults();
  cfg.name = name || basename(dir);
  if (existsSync(join(dir, "agent.py"))) {
    cfg.language = "python";
    cfg.entry = "agent.py:agent";
  } else {
    for (const f of ["agent.mjs", "agent.ts", "agent.js"]) {
      if (existsSync(join(dir, f))) {
        cfg.language = "javascript";
        cfg.entry = `${f}:agent`;
        break;
      }
    }
  }
  return cfg;
}

function parseScalar(raw: string): string | number | boolean {
  const s = raw.trim();
  if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) {
    return s.slice(1, -1);
  }
  if (s === "true" || s === "false") return s === "true";
  if (/^-?\d+$/.test(s)) return parseInt(s, 10);
  return s;
}

export function load(path = ""): Config | null {
  const p = path || find();
  if (!p || !existsSync(p)) return null;
  const cfg = defaults();
  const text = readFileSync(p, "utf8");
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (!t || t.startsWith("#") || t.startsWith("[")) continue;
    const eq = t.indexOf("=");
    if (eq < 0) continue;
    const key = t.slice(0, eq).trim();
    let value = t.slice(eq + 1);
    if (!value.trim().startsWith('"') && !value.trim().startsWith("'")) {
      value = value.split("#")[0]; // strip inline comment on unquoted scalars
    }
    if ((ORDER as readonly string[]).includes(key)) {
      (cfg as unknown as Record<string, unknown>)[key] = String(parseScalar(value));
    }
  }
  return cfg;
}

function tomlEscape(value: string): string {
  return '"' + value.replace(/\\/g, "\\\\").replace(/"/g, '\\"') + '"';
}

export function dumps(cfg: Config): string {
  const lines: string[] = [];
  for (const key of ORDER) {
    const val = (cfg as unknown as Record<string, string>)[key];
    if (key === "agent_id" && !val) continue; // omit until we have one
    lines.push(`${key} = ${tomlEscape(val)}`);
  }
  return lines.join("\n") + "\n";
}

export function save(cfg: Config, directory = ""): string {
  const d = resolve(directory || process.cwd());
  mkdirSync(d, { recursive: true });
  const path = join(d, CONFIG_NAME);
  writeFileSync(path, dumps(cfg));
  return path;
}

/** Persist a resolved agent_id into an existing config. No-op if absent/unchanged. */
export function setAgentId(agentId: string, path = ""): boolean {
  const p = path || find();
  if (!p) return false;
  const cfg = load(p);
  if (!cfg || cfg.agent_id === agentId) return false;
  cfg.agent_id = agentId;
  save(cfg, dirname(p));
  return true;
}

/** Return a list of human-readable problems (empty = valid). */
export function validate(cfg: Config): string[] {
  const problems: string[] = [];
  if (!cfg.name) problems.push("`name` is empty");
  if (!(MODES as readonly string[]).includes(cfg.mode)) {
    problems.push(`\`mode\` must be one of ${MODES.join("|")} (got ${cfg.mode})`);
  }
  if (!(KNOWN_ARENAS as readonly string[]).includes(cfg.arena)) {
    problems.push(`\`arena\` ${cfg.arena} is not a known arena`);
  }
  return problems;
}
