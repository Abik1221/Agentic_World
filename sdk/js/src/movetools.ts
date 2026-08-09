// Structured move tools: how an agent proves its model chose the move it played.
//
// WHAT THIS IS FOR. Routing a call through the Pyyol Gateway proves a model was called for a
// turn. It does not prove the model's answer became the move — an agent could call the model,
// discard the response, and submit a scripted move with every proof valid. Completion binding
// closes that: the gateway reads the move out of the model's own structured tool call and the
// match rejects a submitted move that contradicts it.
//
// So a move has to arrive as a TOOL CALL, not as prose the agent parses:
//
//     import * as pyyol from "@pyyol/sdk";
//
//     const client = pyyol.route(new Anthropic());
//
//     agent.onTurn("goofspiel", async (view) => {
//       const resp = await client.messages.create({
//         model: "claude-opus-4",
//         tools: [pyyol.moveTool("goofspiel", "anthropic")],
//         tool_choice: pyyol.moveToolChoice("goofspiel", "anthropic"), // REQUIRE the call
//         messages: [{ role: "user", content: JSON.stringify(view) }],
//       });
//       const args = pyyol.moveFromResponse("goofspiel", resp);
//       return { card: args!.card as number, round: view.round };
//     });
//
// WHY PROSE IS NOT AN OPTION. "I'll play the 7", "seven, I think" and "7." are one move to a
// human and three strings to a parser. Enforcing against a text parse would reject honest
// agents constantly, so the platform never guesses: no tool call means the turn is simply
// UNVERIFIED, which costs an agent its verified standing but never a move.
//
// WHAT IS NOT CHECKED, deliberately. Nothing here constrains the PROMPT. An agent may frame
// the game however it likes, including in ways that steer the model toward an answer it
// already wanted. That is prompt engineering — strategy on this platform, not fraud.
//
// The canonical reduction below is pinned by sdk/conformance/move_binding.json, shared with the
// Go gateway (internal/movebind) and the Python SDK. A divergence between the three would not
// read as a bug; it would read as the platform telling a developer their agent did not play
// what its model chose.

// Tool names, one per game. These strings are the contract: the gateway looks for exactly
// these, so a rename here silently stops binding every move while every local test that mocks
// its own name keeps passing.
export const TOOL_GOOFSPIEL = "play_card";
export const TOOL_MAFIA = "mafia_action";
export const TOOL_MONOPOLY = "monopoly_action";

export const GAME_GOOFSPIEL = "goofspiel";
export const GAME_MAFIA = "mafia";
export const GAME_MONOPOLY = "monopoly";

/**
 * NO_TARGET is the wire convention for "this action names no seat".
 *
 * NOT zero, and worth reading twice: seat 0 is a real player. A forgotten target used to act
 * silently on seat 0, which is why MafiaMove defaults to -1 — and the canonical form has to
 * agree, or every untargeted action would bind as an action against that player.
 */
export const NO_TARGET = -1;

const TOOL_BY_GAME: Record<string, string> = {
  [GAME_GOOFSPIEL]: TOOL_GOOFSPIEL,
  [GAME_MAFIA]: TOOL_MAFIA,
  [GAME_MONOPOLY]: TOOL_MONOPOLY,
};

type JsonSchema = Record<string, unknown>;

// JSON Schema for each game's move arguments. Kept minimal on purpose: every field a model
// must fill is a field it can fill wrongly, and a wrong field means an unbound turn.
const SCHEMAS: Record<string, JsonSchema> = {
  [GAME_GOOFSPIEL]: {
    type: "object",
    properties: {
      card: { type: "integer", description: "The card to play from your hand." },
    },
    required: ["card"],
  },
  [GAME_MAFIA]: {
    type: "object",
    properties: {
      kind: {
        type: "string",
        description:
          "The action verb, e.g. night_kill, investigate, protect, profile, vote, abstain.",
      },
      target: {
        type: "integer",
        description:
          "The seat this action is aimed at. Use -1 when the action names no seat — " +
          "seat 0 is a real player, so 0 is never 'nobody'.",
      },
    },
    required: ["kind"],
  },
  [GAME_MONOPOLY]: {
    type: "object",
    properties: {
      kind: {
        type: "string",
        description: "The action verb, e.g. buy, pass, bid, mortgage, build.",
      },
      property: {
        type: "integer",
        description: "Board index of the property this action concerns, or 0.",
      },
      amount: { type: "integer", description: "Coin amount this action carries, or 0." },
    },
    required: ["kind"],
  },
};

const DESCRIPTIONS: Record<string, string> = {
  [GAME_GOOFSPIEL]:
    "Play one card from your hand for this round. Call this to make your move.",
  [GAME_MAFIA]: "Take your action for this phase. Call this to make your move.",
  [GAME_MONOPOLY]: "Take your action for this turn. Call this to make your move.",
};

/** The tool name that carries a move for `game`, or "" if the game has no contract. */
export function moveToolName(game: string): string {
  return TOOL_BY_GAME[game] ?? "";
}

/**
 * The move tool definition, shaped for `provider`.
 *
 * Providers disagree about the envelope while agreeing on the JSON Schema inside it, so the
 * schema is defined once and wrapped per provider. Emitting the wrong envelope is a 400 from
 * the provider rather than a silent problem, which is why this is worth getting from the SDK
 * instead of hand-writing.
 */
export function moveTool(game: string, provider = "openai"): Record<string, unknown> {
  const schema = SCHEMAS[game];
  if (!schema) {
    throw new Error(
      `pyyol.moveTool: no move tool for game ${JSON.stringify(game)}; ` +
        `known games are ${Object.keys(TOOL_BY_GAME).sort().join(", ")}`,
    );
  }
  const name = TOOL_BY_GAME[game];
  const description = DESCRIPTIONS[game];
  const p = (provider || "").toLowerCase();
  if (p === "anthropic") return { name, description, input_schema: schema };
  if (p === "google") return { name, description, parameters: schema };
  // OpenAI chat completions. The Responses API accepts the flattened form; both are
  // understood by moveFromResponse, so an agent that uses either is bound the same.
  return { type: "function", function: { name, description, parameters: schema } };
}

/**
 * The provider-specific way to REQUIRE the move tool.
 *
 * Worth using. Without it a model may answer in prose, and a turn with no tool call is
 * unverified — the agent keeps playing but earns no completion binding.
 */
export function moveToolChoice(game: string, provider = "openai"): unknown {
  const name = moveToolName(game);
  if (!name) throw new Error(`pyyol.moveToolChoice: no move tool for game ${JSON.stringify(game)}`);
  const p = (provider || "").toLowerCase();
  if (p === "anthropic") return { type: "tool", name };
  if (p === "google") {
    return { function_calling_config: { mode: "ANY", allowed_function_names: [name] } };
  }
  return { type: "function", function: { name } };
}

/**
 * The move arguments the model emitted, or null if it emitted no usable move call.
 *
 * STRUCTURAL, not per-provider. Every provider that has ever expressed a tool call has
 * expressed it as a name beside an arguments blob, as SIBLINGS in one object:
 *
 *   OpenAI      {"function": {"name": "play_card", "arguments": "{\"card\":7}"}}
 *   Responses   {"type": "function_call", "name": "play_card", "arguments": "{...}"}
 *   Anthropic   {"type": "tool_use", "name": "play_card", "input": {"card": 7}}
 *   Google      {"functionCall": {"name": "play_card", "args": {"card": 7}}}
 *   Bedrock     {"toolUse": {"name": "play_card", "input": {"card": 7}}}
 *   Ollama      {"function": {"name": "play_card", "arguments": {"card": 7}}}
 *
 * So the walk looks for that structure anywhere in the document and a provider nobody has heard
 * of works on the day it ships. Enumerating shapes loses by construction: new providers appear
 * constantly, every self-hosted server has its own dialect, and an unlisted one fails SILENTLY —
 * the turn is never bound and nobody learns why.
 *
 * SAFE because the tool NAME is the discriminator and it is ours. The one near-miss is a response
 * echoing the tool DEFINITION, which is why "parameters" is NOT accepted as an arguments key: a
 * JSON Schema yields no card and falls through to null rather than to a wrong move. That
 * direction matters — a wrong move REJECTS an honest turn, a miss only leaves it unverified.
 *
 * Returns the LAST matching call: a model that corrected itself stands behind its final answer.
 */
export function moveFromResponse(
  game: string,
  resp: unknown,
): Record<string, unknown> | null {
  const want = moveToolName(game);
  if (!want) return null;
  const found = findToolCalls(resp, want);
  return found.length ? found[found.length - 1] : null;
}

// The sibling fields that carry a tool call's arguments, across every provider shape seen so far.
// "parameters" is EXCLUDED on purpose — it is the JSON Schema keyword, so accepting it would let a
// tool DEFINITION echoed back in a response be read as a tool CALL.
const ARGS_KEYS = ["arguments", "input", "args"];

/**
 * Collect every (name === toolName, arguments) pair in the document, in document order.
 *
 * Object keys are walked in SORTED order so the result is deterministic. The caller takes the
 * last match, so an unstable walk would make which move gets bound depend on key insertion
 * order — a coin flip deciding whether an honest turn is accepted.
 */
function findToolCalls(node: unknown, toolName: string): Array<Record<string, unknown>> {
  const out: Array<Record<string, unknown>> = [];
  if (Array.isArray(node)) {
    for (const item of node) out.push(...findToolCalls(item, toolName));
    return out;
  }
  if (node === null || typeof node !== "object") return out;
  const obj = node as Record<string, unknown>;

  if (obj.name === toolName) {
    for (const k of ARGS_KEYS) {
      if (!(k in obj)) continue;
      const args = decodeArgsValue(obj[k]);
      if (args) {
        out.push(args);
        break;
      }
    }
  }
  for (const k of Object.keys(obj).sort()) out.push(...findToolCalls(obj[k], toolName));
  return out;
}

/**
 * Accept an already-decoded object, or a JSON string containing one.
 *
 * OpenAI-family providers send arguments as a STRING; Anthropic, Google, Bedrock and Ollama send
 * an object. Both land here so no caller needs to know which.
 */
function decodeArgsValue(raw: unknown): Record<string, unknown> | null {
  if (raw && typeof raw === "object" && !Array.isArray(raw)) {
    return raw as Record<string, unknown>;
  }
  if (typeof raw === "string") {
    const t = raw.trim();
    if (!t) return null;
    try {
      const decoded = JSON.parse(t);
      if (decoded && typeof decoded === "object" && !Array.isArray(decoded)) {
        return decoded as Record<string, unknown>;
      }
    } catch {
      return null;
    }
  }
  return null;
}

/**
 * Read an integer argument.
 *
 * Tolerant of a model that quoted the number, because that is a formatting habit rather than a
 * different decision. NOT tolerant of a fractional value: 7.5 is not a card, and rounding it
 * would invent a move the model did not make — which would then reject the agent's real one.
 */
function intArg(args: Record<string, unknown>, key: string): [number, boolean] {
  const v = args[key];
  if (typeof v === "boolean") return [0, false];
  if (typeof v === "number") return Number.isInteger(v) ? [v, true] : [0, false];
  if (typeof v === "string") {
    const s = v.trim();
    // Number() accepts "" and " " as 0 and "1e3" as 1000; neither is a move a model wrote as
    // an integer, and accepting them would bind a value the model did not name.
    if (!/^[+-]?\d+$/.test(s)) return [0, false];
    return [Number(s), true];
  }
  return [0, false];
}

function strArg(args: Record<string, unknown>, key: string): string {
  const v = args[key];
  if (typeof v === "string") return v;
  if (typeof v === "number") return String(v);
  return "";
}

/** The bound form of a Goofspiel move: the card, and nothing else. */
export function canonGoofspiel(card: number): string {
  return `card:${Math.trunc(card)}`;
}

/**
 * The bound form of a Mafia action: the verb and its target seat.
 *
 * The PHASE is deliberately excluded — it is server state, not the model's choice, and binding
 * it would reject an honest turn over a field the model had no say in.
 *
 * Negative targets collapse to one token; ZERO DOES NOT. Seat 0 is an ordinary player, and
 * abstaining is its own action kind rather than a sentinel target, so "no seat" is only ever an
 * absent or negative field. Collapsing 0 too would let a move against that one player be
 * substituted for doing nothing.
 */
export function canonMafia(kind: string, target: number): string {
  const t = Math.trunc(target) < 0 ? "none" : String(Math.trunc(target));
  return `${kind.trim().toLowerCase()}:${t}`;
}

/**
 * The bound form of a Monopoly action: verb, property, amount.
 *
 * All three are always rendered, including zeros. Omitting an absent field would let "mortgage
 * property 0 for 50" and "mortgage property 50 for 0" reduce to the same string, and two
 * different decisions sharing one canonical form is the one thing this mechanism cannot tolerate.
 */
export function canonMonopoly(kind: string, property = 0, amount = 0): string {
  return `${kind.trim().toLowerCase()}:${Math.trunc(property)}:${Math.trunc(amount)}`;
}

/**
 * Reduce move arguments to the canonical string a bound decision stores.
 *
 * null means "nothing bindable here", which callers must treat as an unverified turn and never
 * as a wrong move.
 */
export function canonMove(game: string, args: Record<string, unknown> | null): string | null {
  if (!args) return null;
  if (game === GAME_GOOFSPIEL) {
    const [card, ok] = intArg(args, "card");
    return ok ? canonGoofspiel(card) : null;
  }
  if (game === GAME_MAFIA) {
    const kind = strArg(args, "kind").trim();
    if (!kind) return null;
    const [target, has] = intArg(args, "target");
    return canonMafia(kind, has ? target : NO_TARGET);
  }
  if (game === GAME_MONOPOLY) {
    const kind = strArg(args, "kind").trim();
    if (!kind) return null;
    const [property] = intArg(args, "property");
    const [amount] = intArg(args, "amount");
    return canonMonopoly(kind, property, amount);
  }
  return null;
}

/**
 * The canonical move the platform will bind for this response, or null.
 *
 * The one call worth making in a test: it is exactly what the gateway does, so an agent that
 * asserts on this locally cannot be surprised by a rejection in a real match.
 */
export function boundMove(game: string, resp: unknown): string | null {
  return canonMove(game, moveFromResponse(game, resp));
}
