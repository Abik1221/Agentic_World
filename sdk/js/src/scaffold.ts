// Scaffold fingerprinting: what stays the same when you swap the model.
// Mirrors sdk/python/pyyol/scaffold.py — see that file for the full rationale, and
// sdk/conformance/scaffold.json for the fixtures that hold the two to the same bytes.
//
// # The problem this exists to solve
//
// "Claude is better than GPT" is not a claim our data could support before this. Every
// Pyyol match confounds two things: the MODEL a developer chose and the HARNESS they wrote
// around it — the system prompt, the tools, the sampling settings, how the game state is
// framed. A strong agent on a weak model beats a weak agent on a strong model, and the
// leaderboard cannot tell you which happened.
//
// The clean way out is a PAIRED comparison: the same harness, run with model A and with
// model B. Then the harness cancels and the difference is the model.
//
// # What a fingerprint is, and what it is not
//
// A stable id for "the scaffold", derived from what the SDK observes on each model call
// and deliberately EXCLUDING the model name. Excluding the model is the whole point: with
// the model in the hash, every model would get its own scaffold id and nothing could ever
// be paired.
//
// It is not compared across developers, and it is not a way to read anyone's prompt — the
// system prompt enters as a digest, so the fingerprint proves "same prompt" without
// revealing it.

import { createHash } from "node:crypto";

/** Bump only for a change that intentionally invalidates existing fingerprints: it is part
 *  of the hashed payload, so a bump splits every agent's history into a new epoch. */
export const SCAFFOLD_VERSION = "pyyol-scaffold-v1";

/** Sampling parameters that change how a model behaves and are therefore part of the
 *  scaffold. Anything not listed is ignored, so a provider adding an unrelated field does
 *  not silently split every agent's history.
 *
 *  `model` is absent on purpose. So are baseURL, apiKey, timeout and stream: the first two
 *  would make gateway routing look like a new scaffold, and the last two do not affect what
 *  the model decides. */
export const SAMPLING_KEYS: readonly string[] = [
  "frequency_penalty",
  "max_completion_tokens",
  "max_output_tokens",
  "max_tokens",
  "presence_penalty",
  "reasoning_effort",
  "seed",
  "stop",
  "stop_sequences",
  "temperature",
  "thinking",
  "top_k",
  "top_p",
  "verbosity",
];

type Any = Record<string, unknown> | unknown;

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** Canonical number formatting, shared with the Python SDK.
 *
 *  The obvious approach diverges: `String(1.0)` is "1" in JavaScript while Python's
 *  `str(1.0)` is "1.0", so an agent using temperature=1.0 would fingerprint differently in
 *  the two SDKs and its history would split at the language boundary for no reason. Six
 *  significant figures is what both can agree on exactly (Python's "%.6g"). */
function num(v: number | boolean): string {
  if (typeof v === "boolean") return v ? "true" : "false";
  if (!Number.isFinite(v)) return "0";
  if (Number.isInteger(v)) return String(v);
  // toPrecision(6) then back through Number strips the trailing zeros that %.6g also drops.
  return String(Number(v.toPrecision(6)));
}

function scalar(v: unknown): string {
  if (v === null || v === undefined) return "";
  if (typeof v === "number" || typeof v === "boolean") return num(v);
  if (typeof v === "string") return v;
  if (Array.isArray(v)) return "[" + v.map(scalar).join(",") + "]";
  if (isRecord(v)) {
    // A structured value (Anthropic's `thinking: {type, budget_tokens}`). Sorted so key
    // order in the developer's literal cannot change the fingerprint.
    const keys = Object.keys(v).sort();
    return "{" + keys.map((k) => `${k}=${scalar(v[k])}`).join(";") + "}";
  }
  return String(v);
}

/** Flatten a message content field to text.
 *
 *  Only text parts contribute: an image's bytes are not the scaffold, and hashing them
 *  would make the fingerprint depend on the board screenshot of the moment. */
function textOf(content: unknown): string {
  if (content === null || content === undefined) return "";
  if (typeof content === "string") return content;
  if (Array.isArray(content)) return content.map(textOf).join("");
  if (isRecord(content)) {
    const t = content.text;
    return typeof t === "string" ? t : "";
  }
  return "";
}

function digest(text: string): string {
  return createHash("sha256").update(text, "utf8").digest("hex");
}

export type Components = Partial<Record<"client" | "roles" | "sampling" | "tools" | "system", string>>;

/** The fingerprint components observable in one provider request.
 *
 *  Never throws: a fingerprinting problem must not break a developer's model call. */
export function extract(kwargs: Record<string, unknown>, endpoint = ""): Components {
  const roles: string[] = [];
  const systemParts: string[] = [];

  // Anthropic carries the system prompt as a top-level argument rather than a message.
  // Treated as a leading system role so the two providers produce comparable shapes.
  if (kwargs.system !== undefined && kwargs.system !== null) {
    const text = textOf(kwargs.system);
    if (text) {
      roles.push("system");
      systemParts.push(text);
    }
  }

  const messages = kwargs.messages ?? kwargs.input ?? [];
  if (Array.isArray(messages)) {
    for (const m of messages) {
      if (!isRecord(m)) continue;
      const role = typeof m.role === "string" ? m.role : "";
      if (!role) continue;
      roles.push(role);
      // "developer" is OpenAI's newer name for the system role; both are scaffold.
      if (role === "system" || role === "developer") systemParts.push(textOf(m.content));
    }
  }

  const sampling: string[] = [];
  for (const key of SAMPLING_KEYS) {
    const v = kwargs[key];
    if (v !== undefined && v !== null) sampling.push(`${key}=${scalar(v)}`);
  }

  const tools: string[] = [];
  const declared = kwargs.tools;
  if (Array.isArray(declared)) {
    for (const t of declared) {
      let name = "";
      if (isRecord(t)) {
        // OpenAI nests the name under `function`; Anthropic puts it at the top level.
        if (isRecord(t.function) && typeof t.function.name === "string") name = t.function.name;
        if (!name && typeof t.name === "string") name = t.name;
      }
      if (name) tools.push(name);
    }
  }

  const out: Components = {
    client: endpoint,
    roles: roles.join(","),
    sampling: sampling.join(";"),
    // Sorted + deduped: declaring the same tools in a different order is the same scaffold.
    tools: [...new Set(tools)].sort().join(","),
  };
  if (systemParts.length) out.system = digest(systemParts.join("\n"));
  return out;
}

/** The exact string that gets hashed.
 *
 *  Specified rather than incidental: the Python SDK builds the same string and a shared
 *  conformance fixture checks both against the same expected fingerprints. Keys are emitted
 *  in a FIXED order (not sorted, not insertion order) so neither language's map iteration
 *  can affect the result, and an absent component is omitted rather than emitted empty — so
 *  adding a component later does not change the fingerprint of requests that never had one. */
export function canonical(c: Components): string {
  const order = ["client", "roles", "sampling", "tools", "system"] as const;
  const lines: string[] = [SCAFFOLD_VERSION];
  for (const key of order) {
    const val = c[key];
    if (val) lines.push(`${key}=${val}`);
  }
  return lines.join("\n");
}

/** Short, prefixed id for a scaffold. 16 hex chars of SHA-256 (64 bits).
 *
 *  Short enough to read in a UI and group by in SQL. Collisions are irrelevant here in a way
 *  they would not be for a security token: fingerprints are only compared WITHIN one agent's
 *  own history, so the space that must stay distinct is a handful of harness versions. */
export function fingerprint(c: Components): string {
  return "sc_" + digest(canonical(c)).slice(0, 16);
}

/** Why a request could not be fingerprinted, as a stable CODE rather than prose.
 *
 *  A code on the wire and prose at the point of reading, deliberately. The reason repeats on
 *  every decision of every non-qualifying agent, so shipping and storing the sentence would
 *  duplicate it thousands of times per match. A code is also aggregatable — "how many agents
 *  are ineligible, and why" is a question worth being able to ask — and its wording can change
 *  later without a migration. */
export const ISSUE_NO_SYSTEM_PROMPT = "no_system_prompt";
export const ISSUE_NO_MESSAGES = "no_messages";

/** Prose for each code. Read by the CLI and the dev-facing trace; never stored. */
export const ISSUE_EXPLANATIONS: Readonly<Record<string, string>> = {
  [ISSUE_NO_SYSTEM_PROMPT]:
    "This request's instructions live in the user turn, mixed with the game state, where " +
    "they cannot be told apart from it. Move your standing instructions into a system " +
    "message to make this agent eligible for paired model comparison.",
  [ISSUE_NO_MESSAGES]: "No messages were observed on this request, so there was nothing to fingerprint.",
};

/** Code for why this request yields no usable fingerprint, or "" when it does. */
export function issue(kwargs: Record<string, unknown>, endpoint = ""): string {
  let c: Components;
  try {
    c = extract(kwargs, endpoint);
  } catch {
    return ISSUE_NO_MESSAGES;
  }
  if (!c.roles) return ISSUE_NO_MESSAGES;
  if (!c.system) return ISSUE_NO_SYSTEM_PROMPT;
  return "";
}

/** Human-readable reason for an issue code, or "" for no issue / an unknown code. */
export function explain(code: string): string {
  return ISSUE_EXPLANATIONS[code] ?? "";
}

/** Prose reason this request cannot be fingerprinted, or "" when it can.
 *
 *  Convenience for local developer output; the wire carries `issue` codes. */
export function diagnose(kwargs: Record<string, unknown>, endpoint = ""): string {
  return explain(issue(kwargs, endpoint));
}

/** Fingerprint one provider request, or "" when it cannot be fingerprinted.
 *
 *  An empty result means "unknown", never a hash of nothing — a fingerprint shared by every
 *  request that failed to yield components would silently pool unrelated scaffolds into one
 *  bogus epoch and publish it as a controlled comparison.
 *
 *  A SYSTEM PROMPT IS REQUIRED, and this is the sharp edge of the whole design. Without one,
 *  the hashable surface is `client` + `roles` + sampling — none of which move when the
 *  developer rewrites the instructions they actually steer the model with, because those
 *  instructions sit in a user message alongside the game state.
 *
 *  That is worse than having no fingerprint. It is a FALSE CERTIFICATE: an agent could
 *  replace its entire strategy prompt mid-season, keep reporting the same scaffold id, and
 *  have the improvement attributed to whatever model it swapped to — the fingerprint would be
 *  manufacturing the confound it exists to remove. Hashing more cannot fix it, because the
 *  instructions and the game state are the same string. See `diagnose`. */
export function fromRequest(kwargs: Record<string, unknown>, endpoint = ""): string {
  let c: Components;
  try {
    c = extract(kwargs, endpoint);
  } catch {
    return ""; // fingerprinting must never break a model call
  }
  if (!c.system) return "";
  return fingerprint(c);
}

/** True when a set of observations can support a within-scaffold model comparison.
 *
 *  Requires exactly one KNOWN scaffold. An unknown ("") fingerprint disqualifies rather than
 *  being ignored: treating "we could not tell" as "the same as the others" is how a
 *  confounded comparison gets published as a clean one. */
export function eligibleForPairing(fingerprints: ReadonlyArray<string | undefined>): boolean {
  if (!fingerprints.length) return false;
  if (fingerprints.some((f) => !f)) return false;
  return new Set(fingerprints).size === 1;
}

export type { Any };
