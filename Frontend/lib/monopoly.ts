// ---------------------------------------------------------------------------
// Monopoly-inspired AI-vs-AI game — board, types, and a deterministic demo
// simulator that produces a sequence of live "frames" so the spectator UI is
// alive without the backend (mirrors lib/mafia + lib/goofspiel demos).
//
// Rules follow the design doc: 40-tile board, teams accumulate net worth via
// property ownership, building, rent, trades; winner is the last solvent team
// or the highest net worth when the timer expires.
// ---------------------------------------------------------------------------

export type MonoTileType =
  | "start"
  | "property"
  | "rail"
  | "utility"
  | "event"
  | "mystery"
  | "tax"
  | "jail"
  | "parking"
  | "gotojail";

export interface MonoTile {
  id: number;
  name: string;
  short: string;
  type: MonoTileType;
  group?: string; // color group for properties
  color?: string; // hex strip color
  price?: number;
}

export const GROUP_COLOR: Record<string, string> = {
  brown: "#8B5E3C",
  lightblue: "#6FB7E0",
  pink: "#D6489B",
  orange: "#E8821E",
  red: "#E0382E",
  yellow: "#F2C81F",
  green: "#2FA84F",
  blue: "#2A5CC8",
  rail: "#4B5563",
  utility: "#0EA5A0",
};

const P = (id: number, name: string, short: string, group: string, price: number): MonoTile => ({
  id,
  name,
  short,
  type: "property",
  group,
  color: GROUP_COLOR[group],
  price,
});

export const MONO_BOARD: MonoTile[] = [
  { id: 0, name: "GO", short: "GO", type: "start" },
  P(1, "Mediterranean Ave", "Medit.", "brown", 60),
  { id: 2, name: "Community Chest", short: "Chest", type: "mystery" },
  P(3, "Baltic Ave", "Baltic", "brown", 60),
  { id: 4, name: "Income Tax", short: "Tax", type: "tax", price: 200 },
  { id: 5, name: "Reading Railroad", short: "Reading", type: "rail", color: GROUP_COLOR.rail, price: 200 },
  P(6, "Oriental Ave", "Oriental", "lightblue", 100),
  { id: 7, name: "Chance", short: "Chance", type: "event" },
  P(8, "Vermont Ave", "Vermont", "lightblue", 100),
  P(9, "Connecticut Ave", "Connect.", "lightblue", 120),
  { id: 10, name: "Jail / Just Visiting", short: "Jail", type: "jail" },
  P(11, "St. Charles Pl", "St.Chas", "pink", 140),
  { id: 12, name: "Electric Company", short: "Electric", type: "utility", color: GROUP_COLOR.utility, price: 150 },
  P(13, "States Ave", "States", "pink", 140),
  P(14, "Virginia Ave", "Virginia", "pink", 160),
  { id: 15, name: "Pennsylvania RR", short: "Penn RR", type: "rail", color: GROUP_COLOR.rail, price: 200 },
  P(16, "St. James Pl", "St.James", "orange", 180),
  { id: 17, name: "Community Chest", short: "Chest", type: "mystery" },
  P(18, "Tennessee Ave", "Tenn.", "orange", 180),
  P(19, "New York Ave", "New York", "orange", 200),
  { id: 20, name: "Free Parking", short: "Parking", type: "parking" },
  P(21, "Kentucky Ave", "Kentucky", "red", 220),
  { id: 22, name: "Chance", short: "Chance", type: "event" },
  P(23, "Indiana Ave", "Indiana", "red", 220),
  P(24, "Illinois Ave", "Illinois", "red", 240),
  { id: 25, name: "B&O Railroad", short: "B&O RR", type: "rail", color: GROUP_COLOR.rail, price: 200 },
  P(26, "Atlantic Ave", "Atlantic", "yellow", 260),
  P(27, "Ventnor Ave", "Ventnor", "yellow", 260),
  { id: 28, name: "Water Works", short: "Water", type: "utility", color: GROUP_COLOR.utility, price: 150 },
  P(29, "Marvin Gardens", "Marvin", "yellow", 280),
  { id: 30, name: "Go To Jail", short: "Go Jail", type: "gotojail" },
  P(31, "Pacific Ave", "Pacific", "green", 300),
  P(32, "N. Carolina Ave", "N.Car.", "green", 300),
  { id: 33, name: "Community Chest", short: "Chest", type: "mystery" },
  P(34, "Pennsylvania Ave", "Penn Ave", "green", 320),
  { id: 35, name: "Short Line RR", short: "Short Ln", type: "rail", color: GROUP_COLOR.rail, price: 200 },
  { id: 36, name: "Chance", short: "Chance", type: "event" },
  P(37, "Park Place", "Park Pl", "blue", 350),
  { id: 38, name: "Luxury Tax", short: "Lux Tax", type: "tax", price: 100 },
  P(39, "Boardwalk", "Boardwk", "blue", 400),
];

export interface MonoTeam {
  id: number;
  name: string;
  color: string; // hex token color
  agents: string[];
  cash: number;
  position: number;
  properties: number[];
  houses: Record<number, number>;
  netWorth: number;
  bankrupt: boolean;
  inJail: boolean;
}

export type MonoLogType = "Buy" | "Rent" | "Build" | "Trade" | "Tax" | "Move" | "Card" | "Jail";

export interface MonoLogEntry {
  seq: number;
  team: string;
  teamColor: string;
  action: string;
  detail: string;
  type: MonoLogType;
  round: number;
}

export interface MonoFrame {
  teams: MonoTeam[];
  turnTeam: number;
  dice: [number, number];
  round: number;
  phase: string;
  log: MonoLogEntry[];
  winner?: number;
}

const TEAM_DEFS = [
  { name: "Crimson", color: "#FF5A5F", agents: ["ATLAS", "ORACLE"] },
  { name: "Azure", color: "#5C84C8", agents: ["VOID", "SHIVA"] },
  { name: "Emerald", color: "#34D399", agents: ["KARMA", "HEX"] },
  { name: "Gold", color: "#F7B733", agents: ["PRISM", "GHOST"] },
];

function mulberry32(seed: number) {
  return function () {
    seed |= 0;
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function netWorthOf(t: MonoTeam): number {
  let nw = t.cash;
  for (const id of t.properties) {
    const tile = MONO_BOARD[id];
    nw += tile.price ?? 0;
    nw += (t.houses[id] ?? 0) * 50;
  }
  return nw;
}

function ownerOf(teams: MonoTeam[], tileId: number): MonoTeam | undefined {
  return teams.find((t) => t.properties.includes(tileId));
}

function clone(teams: MonoTeam[]): MonoTeam[] {
  return teams.map((t) => ({ ...t, properties: [...t.properties], houses: { ...t.houses }, agents: [...t.agents] }));
}

/** Run a deterministic match and return per-action frames for the spectator UI. */
export function simulateMonopoly(steps = 170, seed = 1337): MonoFrame[] {
  const rng = mulberry32(seed);
  const roll = () => 1 + Math.floor(rng() * 6);

  const teams: MonoTeam[] = TEAM_DEFS.map((d, i) => ({
    id: i,
    name: d.name,
    color: d.color,
    agents: d.agents,
    cash: 1500,
    position: 0,
    properties: [],
    houses: {},
    netWorth: 1500,
    bankrupt: false,
    inJail: false,
  }));

  const frames: MonoFrame[] = [];
  const log: MonoLogEntry[] = [];
  let seq = 0;
  let turn = 0;
  let round = 1;

  const pushLog = (team: MonoTeam, action: string, detail: string, type: MonoLogType) => {
    log.unshift({ seq: seq++, team: team.name, teamColor: team.color, action, detail, type, round });
    if (log.length > 60) log.pop();
  };

  const snapshot = (dice: [number, number], phase: string, winner?: number) => {
    teams.forEach((t) => (t.netWorth = netWorthOf(t)));
    frames.push({ teams: clone(teams), turnTeam: turn, dice, round, phase, log: log.slice(0, 40), winner });
  };

  snapshot([1, 1], "Match start");

  for (let step = 0; step < steps; step++) {
    const alive = teams.filter((t) => !t.bankrupt);
    if (alive.length <= 1) {
      snapshot([1, 1], "Match complete", alive[0]?.id);
      break;
    }
    // advance to next non-bankrupt team
    let guard = 0;
    while (teams[turn].bankrupt && guard++ < 8) turn = (turn + 1) % teams.length;
    const t = teams[turn];

    const d: [number, number] = [roll(), roll()];
    const move = d[0] + d[1];
    const from = t.position;
    let pos = (from + move) % 40;
    if (pos < from) {
      t.cash += 200;
      pushLog(t, "Passed GO", "+200 collected", "Move");
    }
    t.position = pos;
    const tile = MONO_BOARD[pos];
    pushLog(t, "Rolled " + move, `${d[0]} + ${d[1]} → ${tile.name}`, "Move");
    snapshot(d, `${t.name} rolling`);

    // resolve tile
    if (tile.type === "property" || tile.type === "rail" || tile.type === "utility") {
      const owner = ownerOf(teams, pos);
      const price = tile.price ?? 100;
      if (!owner) {
        if (t.cash > price * 1.25 && rng() > 0.18) {
          t.cash -= price;
          t.properties.push(pos);
          pushLog(t, "Bought " + tile.short, `${tile.name} for ${price}`, "Buy");
        } else {
          pushLog(t, "Passed on " + tile.short, "Declined to buy", "Move");
        }
      } else if (owner.id !== t.id) {
        const houses = owner.houses[pos] ?? 0;
        let rent =
          tile.type === "rail"
            ? 25 * Math.max(1, owner.properties.filter((p) => MONO_BOARD[p].type === "rail").length)
            : tile.type === "utility"
              ? move * 4
              : Math.round(price * 0.1 * (1 + houses * 0.8));
        rent = Math.min(rent, t.cash);
        t.cash -= rent;
        owner.cash += rent;
        pushLog(t, "Paid rent", `${rent} to ${owner.name} · ${tile.short}`, "Rent");
        if (t.cash < 0) {
          t.bankrupt = true;
          pushLog(t, "Bankrupt", `${t.name} is out of the match`, "Jail");
        }
      }
    } else if (tile.type === "tax") {
      const tax = tile.price ?? 100;
      t.cash -= tax;
      pushLog(t, "Paid tax", `${tile.name} −${tax}`, "Tax");
    } else if (tile.type === "gotojail") {
      t.position = 10;
      t.inJail = true;
      pushLog(t, "Go to jail", "Sent to jail", "Jail");
    } else if (tile.type === "event" || tile.type === "mystery") {
      const amt = Math.round((rng() - 0.4) * 150);
      t.cash += amt;
      pushLog(t, "Drew a card", `${tile.name}: ${amt >= 0 ? "+" : ""}${amt}`, "Card");
    } else {
      t.inJail = false;
    }
    snapshot(d, `${t.name} resolving`);

    // occasional build on an owned property
    if (t.properties.length > 0 && t.cash > 300 && rng() > 0.6) {
      const target = t.properties[Math.floor(rng() * t.properties.length)];
      if (MONO_BOARD[target].type === "property" && (t.houses[target] ?? 0) < 4) {
        t.houses[target] = (t.houses[target] ?? 0) + 1;
        t.cash -= 100;
        pushLog(t, "Built house", `${MONO_BOARD[target].short} → ${t.houses[target]} house(s)`, "Build");
        snapshot(d, `${t.name} building`);
      }
    }

    // occasional trade (cosmetic)
    if (rng() > 0.9 && alive.length > 1) {
      const other = alive.filter((x) => x.id !== t.id)[Math.floor(rng() * (alive.length - 1))];
      if (other) pushLog(t, "Proposed trade", `Offer to ${other.name} reviewed`, "Trade");
    }

    turn = (turn + 1) % teams.length;
    if (turn === 0) round++;
  }

  if (frames.length === 0) snapshot([1, 1], "Match start");
  return frames;
}

/** Square-ring grid cell (1-indexed row/col on an 11×11 grid) for a tile. */
export function tileCell(i: number): { r: number; c: number } {
  if (i <= 10) return { r: 11, c: 11 - i };
  if (i <= 20) return { r: 11 - (i - 10), c: 1 };
  if (i <= 30) return { r: 1, c: 1 + (i - 20) };
  return { r: 1 + (i - 30), c: 11 };
}

export const MONO_RULES = [
  "Roll, move, and resolve the tile you land on.",
  "Buy unowned properties; collect rent when rivals land on yours.",
  "Build houses on full color groups to raise rent.",
  "Trade, mortgage, and manage cash to stay solvent.",
  "Pass GO for +200. Taxes and cards swing your balance.",
  "Win by bankrupting rivals or holding the highest net worth at the timer.",
];
