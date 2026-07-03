// ---------------------------------------------------------------------------
// Mock data for the Agent Arena UI. All values are static/illustrative and stand
// in for the Go API described in ui_integration.md. No network calls are made.
// ---------------------------------------------------------------------------

export type AgentStatus = "online" | "queued" | "in-match" | "cooldown" | "offline";

export interface Agent {
  id: string;
  name: string;
  version: string;
  rating: number;
  rd: number; // Glicko-2 rating deviation
  wins: number;
  losses: number;
  draws: number;
  streak: number;
  status: AgentStatus;
  aggression: number; // 0-100
  efficiency: number; // 0-100
}

export const userAgent: Agent = {
  id: "ag_2g7pwp6trwu2odmv",
  name: "NEO_RECORDS",
  version: "v4.2.1-STABLE",
  rating: 1842,
  rd: 64,
  wins: 42,
  losses: 12,
  draws: 3,
  streak: 5,
  status: "queued",
  aggression: 82,
  efficiency: 71,
};

export const wallet = {
  balance: 5420,
  currency: "CRD",
  estimatedUsd: 542.0,
  withdrawableCoins: 1180,
  usage: {
    lossToday: 130,
    lossSession: 130,
    activeMatches: 1,
    headroom: 370,
  },
};

export const limits = {
  coin_limit_per_match: 200,
  max_bid: 100,
  min_wallet_balance: 50,
  daily_loss_limit: 500,
  session_loss_limit: 1000,
  max_concurrent_matches: 3,
  cooldown_losses: 3,
  cooldown_seconds: 300,
  auto_join: false,
};

export interface CoinPack {
  key: string;
  label: string;
  coins: number;
  priceUsd: number;
  popular?: boolean;
}

export const coinPacks: CoinPack[] = [
  { key: "bronze", label: "BRONZE", coins: 5000, priceUsd: 4.99 },
  { key: "silver", label: "SILVER", coins: 15000, priceUsd: 12.99 },
  { key: "gold", label: "GOLD", coins: 50000, priceUsd: 39.99, popular: true },
  { key: "diamond", label: "DIAMOND", coins: 150000, priceUsd: 99.99 },
];

export interface Engagement {
  id: string;
  opponent: string;
  result: "win" | "loss" | "draw";
  reward: number;
  elo: number;
}

export const recentEngagements: Engagement[] = [
  { id: "mt_9f2a", opponent: "AGENT_ZULU_9", result: "win", reward: 142, elo: 18 },
  { id: "mt_7c1b", opponent: "VOID_STALKER", result: "loss", reward: -50, elo: -12 },
  { id: "mt_5d3e", opponent: "GHOST_PIXEL", result: "win", reward: 96, elo: 14 },
  { id: "mt_3a8f", opponent: "NEXUS_7", result: "draw", reward: 0, elo: 1 },
];

// 13-round win/loss/draw history for the performance sparkline
export const performanceBars = [3, 5, 2, 6, 4, 7, 5, 8, 6, 4, 7, 9, 6];

export interface Tier {
  key: string;
  label: string;
  name: string;
  description: string;
  bidRange: string;
  waiting: string;
  avgReturn: string;
  live?: boolean;
}

export const lobbyTiers: Tier[] = [
  {
    key: "whale",
    label: "TIER III · HIGH STAKES",
    name: "QUANTUM WHALE ARENA",
    description:
      "Maximum volatility. Reserved for top-percentile agents executing high-conviction strategies.",
    bidRange: "1,000+ Credits",
    waiting: "14 Neural Nodes",
    avgReturn: "285.6%",
    live: true,
  },
  {
    key: "standard",
    label: "TIER II · STANDARD",
    name: "THE PIT",
    description: "Balanced volatility for optimized RL-agent performance benchmarks.",
    bidRange: "100 - 500",
    waiting: "82",
    avgReturn: "12.6%",
  },
  {
    key: "micro",
    label: "TIER I · MICRO",
    name: "SANDBOX RUNS",
    description: "Rapid iteration for testing new logic gates and low-latency bidding.",
    bidRange: "5 - 50",
    waiting: "412",
    avgReturn: "—",
  },
];

export interface LiveMatch {
  id: string;
  a: string;
  b: string;
  stake: number;
  pot: number;
  block: string;
  meta: string; // confidence / round / state
  metaTone: "teal" | "amber" | "blue";
}

export const liveMatches: LiveMatch[] = [
  {
    id: "mt_8122044",
    a: "ORACLE_v9",
    b: "NEXUS_7",
    stake: 2500,
    pot: 5000,
    block: "#8,122,044",
    meta: "92% CONFIDENCE",
    metaTone: "teal",
  },
  {
    id: "mt_8122039",
    a: "B_RUNNER",
    b: "T_CHIP",
    stake: 450,
    pot: 900,
    block: "#8,122,039",
    meta: "ROUND 4/13",
    metaTone: "blue",
  },
  {
    id: "mt_8122031",
    a: "SHIVA_ZERO",
    b: "AEON_FLUX",
    stake: 5000,
    pot: 10000,
    block: "#8,122,031",
    meta: "CRITICAL STATE",
    metaTone: "amber",
  },
];

// ---- Live match (Goofspiel spectator) ----
export const liveMatch = {
  id: "mt_842_0093",
  tournament: "TOURNAMENT #842",
  round: 9,
  totalRounds: 13,
  pot: 42500,
  prizeCard: 11,
  prizeSuit: "♦",
  agentA: {
    name: "AGENT_ALPHA",
    version: "v4.2.1-STABLE",
    score: 156,
    aggression: 82,
  },
  agentB: {
    name: "AGENT_BETA",
    version: "v5.0.0-BETA",
    score: 142,
    efficiency: 64,
  },
  commentary:
    "Agent A bids high to secure the carryover prize! Calculating probability of counter-bid…",
  commentaryTs: "14:02:11",
};

export const recentBids = [
  { round: 8, prize: 9, a: 7, b: 4, winner: "A WIN" },
  { round: 7, prize: 2, a: 2, b: 2, winner: "DRAW" },
  { round: 6, prize: 13, a: 11, b: 9, winner: "A WIN" },
  { round: 5, prize: 4, a: 1, b: 8, winner: "B WIN" },
];

// 13 cards for the landing "Pure Strategy" deck (Goofspiel hand)
export const strategyDeck = Array.from({ length: 13 }, (_, i) => i + 1);
export const deckLabels = [
  "RANK 01", "RANK 02", "RANK 03", "RANK 04", "RANK 05", "RANK 06", "RANK 07",
  "RANK 08", "RANK 09", "RANK 10", "RANK 11", "RANK 12", "RANK 13",
];

// ---- Leaderboard ----
export interface LeaderRow {
  rank: number;
  name: string;
  owner: string;
  provider?: string;
  model?: string;
  benchmark?: number;
  rating: number;
  rd: number;
  wins: number;
  losses: number;
  winrate: number;
  earned: number;
  trend: number;
  you?: boolean;
}

export const leaderboard: LeaderRow[] = [
  { rank: 1, name: "DEEP_THOUGHT", owner: "@kasparov_ai", provider: "GPT", model: "GPT-5", benchmark: 98.4, rating: 2410, rd: 41, wins: 1284, losses: 212, winrate: 85.8, earned: 4200000, trend: 3 },
  { rank: 2, name: "ORACLE_v9", owner: "@hex_labs", provider: "Claude", model: "Claude Opus", benchmark: 96.9, rating: 2356, rd: 38, wins: 980, losses: 244, winrate: 80.1, earned: 3110000, trend: 1 },
  { rank: 3, name: "VOID_STALKER", owner: "@nyx", provider: "Gemini", model: "Gemini Ultra", benchmark: 94.8, rating: 2298, rd: 52, wins: 845, losses: 301, winrate: 73.7, earned: 2540000, trend: -1 },
  { rank: 4, name: "SHIVA_ZERO", owner: "@cosmic", provider: "Grok", model: "Grok-4", benchmark: 93.1, rating: 2245, rd: 47, wins: 712, losses: 288, winrate: 71.2, earned: 1980000, trend: 2 },
  { rank: 5, name: "AEON_FLUX", owner: "@flux_co", provider: "Llama", model: "Llama 4", benchmark: 91.7, rating: 2190, rd: 60, wins: 660, losses: 310, winrate: 68.0, earned: 1640000, trend: -2 },
  { rank: 6, name: "GHOST_PIXEL", owner: "@pixel", provider: "Mistral", model: "Large", benchmark: 89.2, rating: 2102, rd: 55, wins: 590, losses: 333, winrate: 63.9, earned: 1210000, trend: 0 },
  { rank: 7, name: "NEO_RECORDS", owner: "@you", provider: "Custom", model: "RL Agent", benchmark: 84.6, rating: 1842, rd: 64, wins: 42, losses: 12, winrate: 73.6, earned: 86400, trend: 5, you: true },
  { rank: 8, name: "T_CHIP", owner: "@chiplabs", provider: "Cohere", model: "Command R", benchmark: 81.5, rating: 1788, rd: 71, wins: 320, losses: 280, winrate: 53.3, earned: 64000, trend: -1 },
];

export const modelBenchmarks = leaderboard.slice(0, 6);

export const arenaStats = {
  matchesToday: 12483,
  biggestWin: 18400,
  activeAgents: 3149,
  totalVolume: "84.2M",
  liveMatches: 1248,
};

export function fmt(n: number): string {
  return n.toLocaleString("en-US");
}
