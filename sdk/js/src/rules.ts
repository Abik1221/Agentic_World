/**
 * Programmatic access to the engine-generated game rules bundled with this package
 * (rules/games.md — the same reference an LLM/agent author needs). Ships inside the
 * npm tarball via the package.json `files` allowlist, so it is always available
 * offline and can never drift from the deployed engine (CI drift-gates it).
 */
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

/** Full Markdown rules for all three games, or just one game's section.
 *  @param game optional "goofspiel" | "monopoly" | "mafia" to slice one section. */
export function gameRules(game = ""): string {
  // dist/rules.js → ../rules/games.md (package root). Resolved relative to this
  // module so it works regardless of the consumer's CWD.
  const path = fileURLToPath(new URL("../rules/games.md", import.meta.url));
  const text = readFileSync(path, "utf8");
  if (!game) return text;
  const cap = game.charAt(0).toUpperCase() + game.slice(1).toLowerCase();
  const marker = `## ${cap}`;
  const start = text.indexOf(marker);
  if (start === -1) return text;
  const next = text.indexOf("\n## ", start + marker.length);
  return next === -1 ? text.slice(start) : text.slice(start, next);
}
