/** Universal agent identity — every agent is a character, never plain text. */

export type AgentPersonality =
  | "Strategist"
  | "Detective"
  | "Hacker"
  | "Commander"
  | "Scientist"
  | "Guardian"
  | "Shadow"
  | "Aggressive";

export type AgentStatus =
  | "alive"
  | "dead"
  | "suspected"
  | "voting"
  | "speaking"
  | "thinking";

export interface AgentProfile {
  id: number;
  name: string;
  owner: string;
  rank: number;
  winRate: number;
  personality: AgentPersonality;
  /** Tailwind accent token: teal | blue | amber | red | violet | rose */
  accent: "teal" | "blue" | "amber" | "red" | "violet" | "rose";
  provider?: string;
  model?: string;
}

const OWNERS = [
  "@nahom_ai", "@hex_labs", "@kasparov_ai", "@nova_ops", "@cipher_dev",
  "@atlas_team", "@flux_run", "@ghost_net", "@neo_rl", "@chip_co",
  "@warden_sec", "@null_io", "@karma_node", "@prism_echo",
];

const PERSONALITIES: AgentPersonality[] = [
  "Strategist", "Detective", "Hacker", "Commander", "Scientist",
  "Guardian", "Shadow", "Aggressive", "Strategist", "Hacker",
  "Commander", "Shadow", "Guardian", "Scientist",
];

const ACCENTS: AgentProfile["accent"][] = [
  "teal", "blue", "violet", "rose", "amber", "teal", "red", "blue",
  "amber", "violet", "red", "rose", "teal", "blue",
];

/** Build a rich profile from a seat id + display name (demo data until API enriches). */
export function profileFor(id: number, name: string, extras?: Partial<AgentProfile>): AgentProfile {
  const i = Math.max(0, id - 1);
  return {
    id,
    name,
    owner: OWNERS[i % OWNERS.length],
    rank: Math.max(1, 14 - i + Math.floor(id / 3)),
    winRate: 52 + ((id * 7) % 35),
    personality: PERSONALITIES[i % PERSONALITIES.length],
    accent: ACCENTS[i % ACCENTS.length],
    ...extras,
  };
}

export const STATUS_LABEL: Record<AgentStatus, string> = {
  alive: "Alive",
  dead: "Eliminated",
  suspected: "Suspected",
  voting: "Voting",
  speaking: "Speaking",
  thinking: "Thinking",
};

export function statusTone(status: AgentStatus): "teal" | "amber" | "blue" | "red" | "neutral" {
  switch (status) {
    case "alive":
      return "teal";
    case "speaking":
      return "blue";
    case "voting":
      return "amber";
    case "suspected":
      return "red";
    case "thinking":
      return "blue";
    default:
      return "neutral";
  }
}
