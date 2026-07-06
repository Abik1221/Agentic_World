// Scripted demo data for the Monopoly live-match viewer. A premium 40-space
// board, four AI "executives" trading, buying, and building. Self-runs so the
// negotiation spectacle is alive without a backend.

import { AGENTS as ROSTER } from "./mafia-demo";

export type MIntent =
  | "planning"
  | "trade"
  | "roi"
  | "threat"
  | "liquidity"
  | "build"
  | "expansion"
  | "monopoly"
  | "defensive";

export const MINTENT: Record<MIntent, { icon: string; label: string; color: string }> = {
  planning: { icon: "📈", label: "Planning", color: "#6366f1" },
  trade: { icon: "🤝", label: "Trade", color: "#22c55e" },
  roi: { icon: "📊", label: "ROI Analysis", color: "#8b5cf6" },
  threat: { icon: "⚠", label: "Threat", color: "#f59e0b" },
  liquidity: { icon: "🏦", label: "Liquidity", color: "#14b8a6" },
  build: { icon: "🏗", label: "Building", color: "#ec4899" },
  expansion: { icon: "🧠", label: "Expansion", color: "#6366f1" },
  monopoly: { icon: "🎯", label: "Monopoly", color: "#22c55e" },
  defensive: { icon: "🛡", label: "Defensive", color: "#818cf8" },
};

export type MAgent = { id: string; name: string; dev: string; color: string; model: string; sdk: string; winRate: number; startCash: number };

export const MAGENTS: MAgent[] = ROSTER.slice(0, 4).map((a, i) => ({
  id: a.id,
  name: a.name,
  dev: a.dev,
  color: a.color,
  model: a.model,
  sdk: a.sdk,
  winRate: a.winRate,
  startCash: [1200, 1400, 1300, 1100][i],
}));

export type SpaceType = "go" | "prop" | "rail" | "util" | "tax" | "chance" | "chest" | "jail" | "gotojail" | "parking";
export type Space = { i: number; type: SpaceType; name: string; price?: number; group?: string };

const G = {
  brown: "#8d6e63",
  cyan: "#22b8cf",
  pink: "#ec4899",
  orange: "#f59e0b",
  red: "#ef4444",
  yellow: "#eab308",
  green: "#22c55e",
  blue: "#6366f1",
  rail: "#64748b",
  util: "#14b8a6",
};

// Generic premium districts (trademark-free) on the classic 40-space layout.
export const BOARD: Space[] = [
  { i: 0, type: "go", name: "GO" },
  { i: 1, type: "prop", name: "Old Wharf", price: 60, group: G.brown },
  { i: 2, type: "chest", name: "Reserve" },
  { i: 3, type: "prop", name: "Dock Ln", price: 60, group: G.brown },
  { i: 4, type: "tax", name: "Income Tax" },
  { i: 5, type: "rail", name: "North Rail", price: 200, group: G.rail },
  { i: 6, type: "prop", name: "Bay Ave", price: 100, group: G.cyan },
  { i: 7, type: "chance", name: "Signal" },
  { i: 8, type: "prop", name: "Cyan St", price: 100, group: G.cyan },
  { i: 9, type: "prop", name: "Harbor Rd", price: 120, group: G.cyan },
  { i: 10, type: "jail", name: "Jail" },
  { i: 11, type: "prop", name: "Market Sq", price: 140, group: G.pink },
  { i: 12, type: "util", name: "Power Co", price: 150, group: G.util },
  { i: 13, type: "prop", name: "Pink Ave", price: 140, group: G.pink },
  { i: 14, type: "prop", name: "Rose Blvd", price: 160, group: G.pink },
  { i: 15, type: "rail", name: "East Rail", price: 200, group: G.rail },
  { i: 16, type: "prop", name: "Amber St", price: 180, group: G.orange },
  { i: 17, type: "chest", name: "Reserve" },
  { i: 18, type: "prop", name: "Orange Ave", price: 180, group: G.orange },
  { i: 19, type: "prop", name: "Sunset Rd", price: 200, group: G.orange },
  { i: 20, type: "parking", name: "Free Parking" },
  { i: 21, type: "prop", name: "Ruby St", price: 220, group: G.red },
  { i: 22, type: "chance", name: "Signal" },
  { i: 23, type: "prop", name: "Red Ave", price: 220, group: G.red },
  { i: 24, type: "prop", name: "Crimson Rd", price: 240, group: G.red },
  { i: 25, type: "rail", name: "South Rail", price: 200, group: G.rail },
  { i: 26, type: "prop", name: "Gold St", price: 260, group: G.yellow },
  { i: 27, type: "prop", name: "Yellow Ave", price: 260, group: G.yellow },
  { i: 28, type: "util", name: "Water Co", price: 150, group: G.util },
  { i: 29, type: "prop", name: "Sun Blvd", price: 280, group: G.yellow },
  { i: 30, type: "gotojail", name: "Go To Jail" },
  { i: 31, type: "prop", name: "Emerald St", price: 300, group: G.green },
  { i: 32, type: "prop", name: "Green Ave", price: 300, group: G.green },
  { i: 33, type: "chest", name: "Reserve" },
  { i: 34, type: "prop", name: "Jade Rd", price: 320, group: G.green },
  { i: 35, type: "rail", name: "West Rail", price: 200, group: G.rail },
  { i: 36, type: "chance", name: "Signal" },
  { i: 37, type: "prop", name: "Sapphire Ave", price: 350, group: G.blue },
  { i: 38, type: "tax", name: "Luxury Tax" },
  { i: 39, type: "prop", name: "Boardwalk", price: 400, group: G.blue },
];

// 11×11 grid placement (1-based col/row) for a perimeter space.
export function gridPos(i: number): { col: number; row: number } {
  if (i <= 10) return { col: 11 - i, row: 11 };
  if (i <= 20) return { col: 1, row: 11 - (i - 10) };
  if (i <= 30) return { col: 1 + (i - 20), row: 1 };
  return { col: 11, row: 1 + (i - 30) };
}

export const INITIAL_OWNERS: Record<number, string> = {
  6: "A", 8: "A", 5: "A", // Cascade: cyan pair + a rail
  11: "B", 13: "B", 21: "B", // Vesper: pink pair + red
  16: "C", 18: "C", // Marlow: orange pair
  31: "D", 32: "D", 37: "D", // Juno: green pair + blue
};

export type MStep = {
  turn: number;
  player: string;
  kind: "say" | "roll" | "buy" | "rent" | "trade" | "build";
  dice?: [number, number];
  to?: number;
  buy?: number;
  rent?: { from: string; to: string; amount: number };
  trade?: {
    from: string;
    to: string;
    give: string;
    get: string;
    status: "propose" | "accept" | "reject";
    giveIdx?: number; // property `from` gives to `to` (transferred on accept)
    getIdx?: number; // property `to` gives to `from`
    cash?: number; // net cash `from` pays `to` on accept (negative = `to` pays `from`)
  };
  build?: { spaces: number[] };
  speaker?: string;
  intent?: MIntent;
  text?: string;
  event?: string;
  // LIVE ONLY: authoritative per-seat cash snapshot (from the engine's cash
  // ledger) as of this step. Demo steps omit it, so the viewer keeps deriving
  // cash from step effects; when present it overrides that derivation.
  liveCash?: Record<string, number>;
};

export const MSCRIPT: MStep[] = [
  { turn: 12, player: "A", kind: "say", speaker: "A", intent: "planning", text: "Targeting the orange district — best mid-board ROI on the map.", event: "Turn 12 — Cascade to act" },
  { turn: 12, player: "A", kind: "roll", dice: [3, 4], to: 19, event: "Cascade rolls 7 → Sunset Rd" },
  { turn: 12, player: "A", kind: "buy", buy: 19, event: "Cascade buys Sunset Rd ($200)" },
  { turn: 12, player: "C", kind: "say", speaker: "C", intent: "threat", text: "Cascade is closing on orange right beside my two. I need to block that monopoly.", },
  { turn: 12, player: "C", kind: "trade", trade: { from: "C", to: "A", give: "Amber St + $150", get: "Sunset Rd", status: "propose" }, speaker: "C", intent: "trade", text: "Offering Amber St plus $150 for Sunset Rd.", event: "Marlow proposes a trade to Cascade" },
  { turn: 12, player: "A", kind: "say", speaker: "A", intent: "roi", text: "Holding the orange anchor beats $150 today. Expected value says decline.", },
  { turn: 12, player: "A", kind: "trade", trade: { from: "A", to: "C", give: "", get: "", status: "reject" }, event: "Cascade rejects the trade" },
  { turn: 13, player: "B", kind: "roll", dice: [5, 2], to: 15, event: "Vesper rolls 7 → East Rail" },
  { turn: 13, player: "B", kind: "buy", buy: 15, event: "Vesper buys East Rail ($200)" },
  { turn: 14, player: "D", kind: "roll", dice: [2, 2], to: 21, event: "Juno rolls 4 → Ruby St" },
  { turn: 14, player: "D", kind: "rent", rent: { from: "D", to: "B", amount: 60 }, speaker: "D", intent: "liquidity", text: "Paying $60 rent to Vesper. Cash is tightening — watching liquidity closely.", event: "Juno pays $60 rent to Vesper" },
  { turn: 15, player: "B", kind: "roll", dice: [6, 2], to: 14, event: "Vesper rolls 8 → Rose Blvd" },
  { turn: 15, player: "B", kind: "buy", buy: 14, event: "Vesper buys Rose Blvd — completes pink" },
  { turn: 15, player: "B", kind: "say", speaker: "B", intent: "build", text: "Pink monopoly complete. Building houses to spike rent before the next lap.", },
  { turn: 15, player: "B", kind: "build", build: { spaces: [11, 13, 14] }, event: "Vesper builds houses across the pink group" },
  { turn: 16, player: "A", kind: "say", speaker: "A", intent: "defensive", text: "Vesper's pink is now the board's biggest threat. I'll hoard cash and wait for their squeeze.", },
  { turn: 17, player: "B", kind: "say", speaker: "B", intent: "liquidity", text: "I need cash to keep building. Juno — Ruby St is yours for $260.", trade: { from: "B", to: "D", give: "Ruby St", get: "$260", status: "propose", giveIdx: 21, cash: -260 }, event: "Vesper proposes Ruby St → Juno for $260" },
  { turn: 17, player: "D", kind: "say", speaker: "D", intent: "roi", text: "Ruby anchors my red flank and Vesper stays liquid. Fair value — accepted.", },
  { turn: 17, player: "D", kind: "trade", trade: { from: "B", to: "D", give: "Ruby St", get: "$260", status: "accept", giveIdx: 21, cash: -260 }, event: "Juno accepts — Ruby St ↔ $260" },
  { turn: 18, player: "D", kind: "build", build: { spaces: [21, 21, 21] }, speaker: "D", intent: "monopoly", text: "Stacking houses on Ruby to convert the trade into rent pressure fast.", event: "Juno builds on Ruby St" },
];
