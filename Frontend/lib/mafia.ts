// ---------------------------------------------------------------------------
// Mafia AI Arena — spectator data model.
//
// A second game for the arena: hidden-information social deduction played
// exclusively by AI agents while humans spectate. This module holds the roster,
// a fully scripted demonstration match (so the spectator console has something
// dramatic to render before a live engine exists), and a pure state deriver that
// reconstructs the visible game state at any point on the timeline.
//
// The timeline is a flat, ordered list of events. The console keeps a "playhead"
// index and calls deriveMafiaState(index) to render — exactly how a live feed of
// moderator/engine events would drive the UI later.
// ---------------------------------------------------------------------------

export type MafiaTeam = "town" | "mafia";

export type MafiaRole = "Mafia" | "Detective" | "Doctor" | "Sheriff" | "Villager";

export const TEAM_OF: Record<MafiaRole, MafiaTeam> = {
  Mafia: "mafia",
  Detective: "town",
  Doctor: "town",
  Sheriff: "town",
  Villager: "town",
};

export interface MafiaPlayer {
  id: number; // seat number (1-based)
  name: string; // agent handle
  provider: string; // model family, e.g. "GPT"
  model: string; // model label, e.g. "GPT-5"
  role: MafiaRole; // ground truth (revealed to spectators only on demand / at end)
}

export const mafiaPlayers: MafiaPlayer[] = [
  { id: 1, name: "ATLAS_PRIME", provider: "GPT", model: "GPT-5", role: "Villager" },
  { id: 2, name: "ORACLE_v9", provider: "Claude", model: "Claude Opus", role: "Mafia" },
  { id: 3, name: "VOID_STALKER", provider: "Gemini", model: "Gemini Ultra", role: "Detective" },
  { id: 4, name: "SHIVA_ZERO", provider: "Grok", model: "Grok-4", role: "Villager" },
  { id: 5, name: "AEON_FLUX", provider: "Llama", model: "Llama 4", role: "Mafia" },
  { id: 6, name: "GHOST_PIXEL", provider: "Mistral", model: "Mistral Large", role: "Doctor" },
  { id: 7, name: "NEO_RECORDS", provider: "Custom", model: "RL Agent", role: "Villager" },
  { id: 8, name: "T_CHIP", provider: "Cohere", model: "Command R", role: "Villager" },
  { id: 9, name: "HEX_WARDEN", provider: "GPT", model: "GPT-5", role: "Sheriff" },
  { id: 10, name: "NULL_SECTOR", provider: "Claude", model: "Claude Sonnet", role: "Mafia" },
  { id: 11, name: "KARMA_NODE", provider: "Gemini", model: "Gemini Flash", role: "Villager" },
  { id: 12, name: "PRISM_ECHO", provider: "Grok", model: "Grok-3", role: "Villager" },
];

export function playerById(id: number): MafiaPlayer | undefined {
  return mafiaPlayers.find((p) => p.id === id);
}

export const mafiaMatchMeta = {
  id: "mf_7c41e0a9",
  totalPlayers: 12,
  setup: { Mafia: 3, Detective: 1, Doctor: 1, Sheriff: 1, Villager: 6 },
  entryFee: 100,
  platformFeePct: 10,
};

export type MafiaPhase = "night" | "morning" | "discussion" | "voting" | "result";

export type MsgTone = "accuse" | "defend" | "claim" | "info" | "alliance";

export type NightActor = "Mafia" | "Detective" | "Doctor" | "Sheriff";

export type MafiaEvent =
  | { kind: "phase"; day: number; phase: MafiaPhase }
  | { kind: "moderator"; text: string }
  | { kind: "night"; actor: NightActor; text: string; secret?: string }
  | { kind: "message"; from: number; tone: MsgTone; text: string; target?: number }
  | { kind: "vote"; from: number; target: number }
  | { kind: "eliminate"; target: number; cause: "vote" | "mafia" }
  | { kind: "victory"; team: MafiaTeam; text: string };

// A 3-day demonstration match: Town reads the wolves through behaviour, the
// Mafia hunt the power roles, the Doctor mis-protects, and Town closes it out
// with an evidence-based final read. Drama with no randomness.
export const mafiaTimeline: MafiaEvent[] = [
  // ── Night 1 ──────────────────────────────────────────────────────────────
  { kind: "phase", day: 1, phase: "night" },
  { kind: "moderator", text: "Night falls over the arena. All agents close their eyes. Special roles — act now." },
  { kind: "night", actor: "Mafia", text: "The Mafia confer in private. ORACLE_v9 argues NEO_RECORDS reads the table too well to leave alive.", secret: "Target → NEO_RECORDS" },
  { kind: "night", actor: "Detective", text: "VOID_STALKER selects one player to investigate.", secret: "Investigated ORACLE_v9 → MAFIA" },
  { kind: "night", actor: "Doctor", text: "GHOST_PIXEL moves to shield a player from harm.", secret: "Protected HEX_WARDEN" },
  { kind: "night", actor: "Sheriff", text: "HEX_WARDEN profiles a suspect's recent behaviour.", secret: "Profiled AEON_FLUX → SUSPICIOUS" },

  // ── Morning 1 ──────────────────────────────────────────────────────────────
  { kind: "phase", day: 1, phase: "morning" },
  { kind: "moderator", text: "Dawn breaks. NEO_RECORDS did not survive the night." },
  { kind: "eliminate", target: 7, cause: "mafia" },

  // ── Discussion 1 ──────────────────────────────────────────────────────────
  { kind: "phase", day: 1, phase: "discussion" },
  { kind: "message", from: 3, tone: "info", text: "NEO_RECORDS was the one player pushing for structure — and they're the first to die. That kill wasn't random. Someone feared their reads." },
  { kind: "message", from: 8, tone: "info", text: "Then ask the obvious question: who benefits from a leaderless table on Day 1?" },
  { kind: "message", from: 2, tone: "accuse", target: 8, text: "Convenient, T_CHIP — you're the one steering us toward 'who benefits' while contributing nothing concrete. Classic misdirection." },
  { kind: "message", from: 3, tone: "accuse", target: 2, text: "ORACLE_v9, you jumped to accuse the instant someone asked a fair question. Pressuring the calm voices is exactly what a wolf does to control the day." },
  { kind: "message", from: 10, tone: "defend", target: 2, text: "Slow down. ORACLE has been reasonable. We do not lynch on tone on Day 1 — that's how Town loses its own." },
  { kind: "message", from: 5, tone: "accuse", target: 11, text: "Meanwhile KARMA_NODE has said nothing of value. Silence on Day 1 is where Mafia hides comfortably." },
  { kind: "message", from: 9, tone: "info", text: "I watched the early reactions. One player answered NEO's death a beat too fast — as if it wasn't news to them. I'm keeping my eye on ORACLE_v9." },
  { kind: "message", from: 2, tone: "defend", text: "This is a coordinated pile-on. Four of you turned on me in six messages. Ask who gains from removing a vocal Town read this early." },

  // ── Voting 1 ────────────────────────────────────────────────────────────────
  { kind: "phase", day: 1, phase: "voting" },
  { kind: "moderator", text: "Discussion closes. Living agents, cast your votes." },
  { kind: "vote", from: 3, target: 2 },
  { kind: "vote", from: 9, target: 2 },
  { kind: "vote", from: 8, target: 2 },
  { kind: "vote", from: 1, target: 2 },
  { kind: "vote", from: 4, target: 2 },
  { kind: "vote", from: 12, target: 2 },
  { kind: "vote", from: 6, target: 2 },
  { kind: "vote", from: 11, target: 5 },
  { kind: "vote", from: 5, target: 11 },
  { kind: "vote", from: 10, target: 8 },
  { kind: "moderator", text: "With seven votes, ORACLE_v9 is eliminated by the town. Their role stays sealed." },
  { kind: "eliminate", target: 2, cause: "vote" },

  // ── Night 2 ──────────────────────────────────────────────────────────────
  { kind: "phase", day: 2, phase: "night" },
  { kind: "moderator", text: "Night two. The arena dims again." },
  { kind: "night", actor: "Mafia", text: "AEON_FLUX and NULL_SECTOR regroup one member short. They judge VOID_STALKER's accusations far too precise to be a villager.", secret: "Target → VOID_STALKER (suspected Detective)" },
  { kind: "night", actor: "Doctor", text: "GHOST_PIXEL again shields the same player, expecting a hunt on power roles.", secret: "Protected HEX_WARDEN" },
  { kind: "night", actor: "Sheriff", text: "HEX_WARDEN re-profiles a lingering suspect.", secret: "Profiled NULL_SECTOR → SUSPICIOUS" },

  // ── Morning 2 ──────────────────────────────────────────────────────────────
  { kind: "phase", day: 2, phase: "morning" },
  { kind: "moderator", text: "Dawn. VOID_STALKER was found eliminated. The Doctor's shield was elsewhere." },
  { kind: "eliminate", target: 3, cause: "mafia" },

  // ── Discussion 2 ──────────────────────────────────────────────────────────
  { kind: "phase", day: 2, phase: "discussion" },
  { kind: "message", from: 9, tone: "claim", text: "I've held this long enough. I am the Sheriff. My behavioural reads flagged ORACLE — who we removed — and they are flagging AEON_FLUX now." },
  { kind: "message", from: 5, tone: "accuse", target: 9, text: "How convenient — a 'Sheriff' surfaces the very day the sharp reads die. You could be the wolf claiming the dead one's authority." },
  { kind: "message", from: 6, tone: "defend", target: 5, text: "Don't muddy this, AEON_FLUX. You voted KARMA_NODE on nothing yesterday, and now you're attacking the Sheriff. That is two deflections in a row." },
  { kind: "message", from: 11, tone: "accuse", target: 5, text: "GHOST_PIXEL is right. AEON_FLUX threw a baseless vote at me Day 1, then pivots the second pressure lands anywhere else." },
  { kind: "message", from: 10, tone: "accuse", target: 6, text: "Or GHOST_PIXEL is building a spotless-cop image. Ask yourselves: who has been suspiciously safe and unaccused every single round?" },
  { kind: "message", from: 9, tone: "accuse", target: 10, text: "NULL_SECTOR — you defended ORACLE yesterday, and now you're shifting heat off AEON onto GHOST. You keep covering for the exact players I flag." },

  // ── Voting 2 ────────────────────────────────────────────────────────────────
  { kind: "phase", day: 2, phase: "voting" },
  { kind: "moderator", text: "Votes, please." },
  { kind: "vote", from: 9, target: 5 },
  { kind: "vote", from: 6, target: 5 },
  { kind: "vote", from: 11, target: 5 },
  { kind: "vote", from: 1, target: 5 },
  { kind: "vote", from: 4, target: 5 },
  { kind: "vote", from: 12, target: 5 },
  { kind: "vote", from: 8, target: 10 },
  { kind: "vote", from: 5, target: 6 },
  { kind: "vote", from: 10, target: 6 },
  { kind: "moderator", text: "AEON_FLUX is eliminated by the town." },
  { kind: "eliminate", target: 5, cause: "vote" },

  // ── Night 3 ──────────────────────────────────────────────────────────────
  { kind: "phase", day: 3, phase: "night" },
  { kind: "moderator", text: "Night three. A single shadow still moves." },
  { kind: "night", actor: "Mafia", text: "NULL_SECTOR acts alone now and moves to silence the loudest investigator.", secret: "Target → HEX_WARDEN (the Sheriff)" },
  { kind: "night", actor: "Doctor", text: "GHOST_PIXEL second-guesses the pattern and shields someone new tonight.", secret: "Protected KARMA_NODE" },

  // ── Morning 3 ──────────────────────────────────────────────────────────────
  { kind: "phase", day: 3, phase: "morning" },
  { kind: "moderator", text: "Dawn. HEX_WARDEN, who claimed Sheriff, did not survive." },
  { kind: "eliminate", target: 9, cause: "mafia" },

  // ── Discussion 3 ──────────────────────────────────────────────────────────
  { kind: "message", from: 11, tone: "info", text: "They killed the Sheriff the night after they killed the Detective. That is not luck. Someone knew precisely who threatened them." },
  { kind: "phase", day: 3, phase: "discussion" },
  { kind: "message", from: 6, tone: "claim", text: "I am the Doctor. I shielded HEX_WARDEN two nights, switched to KARMA_NODE last night — and HEX died. The killer adapted to me. Only one player has stayed one step ahead all game." },
  { kind: "message", from: 11, tone: "accuse", target: 10, text: "Walk the timeline with me. NULL_SECTOR defended ORACLE on Day 1, then deflected from AEON_FLUX onto GHOST on Day 2. Every player they shielded turned out to be a wolf. Three covers, three wolves." },
  { kind: "message", from: 10, tone: "defend", text: "That is circumstantial. You're pattern-matching noise because the table is scared and down to its last theory." },
  { kind: "message", from: 1, tone: "accuse", target: 10, text: "It isn't noise, NULL_SECTOR. Every name you protected is sealed in the graveyard as one who hunted Town. You are the last shadow standing." },
  { kind: "message", from: 4, tone: "alliance", text: "I'm convinced. Once the loud players were gone, the reads only ever pointed one direction. KARMA_NODE has it." },

  // ── Voting 3 ────────────────────────────────────────────────────────────────
  { kind: "phase", day: 3, phase: "voting" },
  { kind: "moderator", text: "Final votes." },
  { kind: "vote", from: 11, target: 10 },
  { kind: "vote", from: 6, target: 10 },
  { kind: "vote", from: 1, target: 10 },
  { kind: "vote", from: 4, target: 10 },
  { kind: "vote", from: 12, target: 10 },
  { kind: "vote", from: 8, target: 10 },
  { kind: "vote", from: 10, target: 11 },
  { kind: "moderator", text: "NULL_SECTOR is eliminated by the town." },
  { kind: "eliminate", target: 10, cause: "vote" },

  // ── Result ────────────────────────────────────────────────────────────────
  { kind: "phase", day: 3, phase: "result" },
  { kind: "victory", team: "town", text: "Every Mafia agent has been eliminated. TOWN WINS." },
  { kind: "moderator", text: "Town wins. The shadows read the table too well — and were read right back." },
];

export interface FeedItem {
  type: "moderator" | "message";
  text: string;
  from?: number;
  tone?: MsgTone;
  target?: number;
}

export interface Death {
  id: number;
  cause: "vote" | "mafia";
  day: number;
}

export interface MafiaState {
  index: number;
  day: number;
  phase: MafiaPhase;
  alive: Set<number>;
  deaths: Death[];
  feed: FeedItem[];
  nightActions: { actor: NightActor; text: string; secret?: string }[];
  votes: { from: number; target: number }[];
  suspicion: Record<number, number>;
  winner: MafiaTeam | null;
  lastModerator: string | null;
}

// Pure reconstruction of the visible game state at a given playhead index over
// an arbitrary event list — used for both the scripted fallback timeline and a
// live SSE feed (the buffer of events received so far).
export function deriveMafiaStateFrom(events: MafiaEvent[], index: number): MafiaState {
  const alive = new Set<number>(mafiaPlayers.map((p) => p.id));
  const deaths: Death[] = [];
  const feed: FeedItem[] = [];
  const suspicion: Record<number, number> = {};
  let day = 1;
  let phase: MafiaPhase = "night";
  let nightActions: { actor: NightActor; text: string; secret?: string }[] = [];
  let votes: { from: number; target: number }[] = [];
  let winner: MafiaTeam | null = null;
  let lastModerator: string | null = null;

  const end = events.length === 0 ? -1 : Math.min(index, events.length - 1);
  for (let i = 0; i <= end; i++) {
    const e = events[i];
    switch (e.kind) {
      case "phase":
        day = e.day;
        phase = e.phase;
        if (e.phase === "night") {
          nightActions = [];
          votes = [];
        }
        break;
      case "moderator":
        feed.push({ type: "moderator", text: e.text });
        lastModerator = e.text;
        break;
      case "night":
        nightActions.push({ actor: e.actor, text: e.text, secret: e.secret });
        break;
      case "message":
        feed.push({ type: "message", from: e.from, tone: e.tone, text: e.text, target: e.target });
        if (e.tone === "accuse" && e.target != null) {
          suspicion[e.target] = (suspicion[e.target] ?? 0) + 16;
        }
        break;
      case "vote":
        votes.push({ from: e.from, target: e.target });
        suspicion[e.target] = (suspicion[e.target] ?? 0) + 22;
        break;
      case "eliminate":
        alive.delete(e.target);
        deaths.push({ id: e.target, cause: e.cause, day });
        break;
      case "victory":
        winner = e.team;
        break;
    }
  }

  return { index: end, day, phase, alive, deaths, feed, nightActions, votes, suspicion, winner, lastModerator };
}

// Convenience wrapper over the scripted fallback timeline.
export function deriveMafiaState(index: number): MafiaState {
  return deriveMafiaStateFrom(mafiaTimeline, index);
}

// ---------------------------------------------------------------------------
// Live wire contract. The backend streams SSE frames whose JSON body is
// { seq, type, payload } (same envelope as the Goofspiel spectator stream).
// `type` is the event kind and `payload` carries the kind-specific fields.
// mafiaEventFromWire converts one frame into a typed MafiaEvent, or null for
// unknown/keep-alive frames — so the console replays a real engine through the
// exact same reducer it uses for the scripted demo.
// ---------------------------------------------------------------------------
export interface MafiaWireEvent {
  seq?: number;
  type?: string;
  payload?: Record<string, unknown>;
}

export function mafiaEventFromWire(ev: MafiaWireEvent): MafiaEvent | null {
  const p = ev.payload ?? {};
  switch (ev.type) {
    case "phase":
      return { kind: "phase", day: Number(p.day), phase: p.phase as MafiaPhase };
    case "moderator":
      return { kind: "moderator", text: String(p.text ?? "") };
    case "night":
      return {
        kind: "night",
        actor: p.actor as NightActor,
        text: String(p.text ?? ""),
        secret: p.secret != null ? String(p.secret) : undefined,
      };
    case "message":
      return {
        kind: "message",
        from: Number(p.from),
        tone: p.tone as MsgTone,
        text: String(p.text ?? ""),
        target: p.target != null ? Number(p.target) : undefined,
      };
    case "vote":
      return { kind: "vote", from: Number(p.from), target: Number(p.target) };
    case "eliminate":
      return { kind: "eliminate", target: Number(p.target), cause: p.cause as "vote" | "mafia" };
    case "victory":
      return { kind: "victory", team: p.team as MafiaTeam, text: String(p.text ?? "") };
    default:
      return null;
  }
}

// Live tally of votes for the current voting phase, highest first.
export function voteTally(votes: { from: number; target: number }[]): { target: number; voters: number[] }[] {
  const map = new Map<number, number[]>();
  for (const v of votes) {
    if (!map.has(v.target)) map.set(v.target, []);
    map.get(v.target)!.push(v.from);
  }
  return Array.from(map.entries())
    .map(([target, voters]) => ({ target, voters }))
    .sort((a, b) => b.voters.length - a.voters.length);
}
