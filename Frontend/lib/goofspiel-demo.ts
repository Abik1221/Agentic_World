// Scripted demo data for the Goofspiel live-match viewer. Sealed-bid card duel:
// each round a prize card is revealed, agents secretly bid a card from their hand,
// highest unique bid wins the prize points; ties carry the prize to the next round.
// This self-runs so the reasoning spectacle is alive without a backend.

import { AGENTS as MAFIA_AGENTS } from "./mafia-demo";

export type GIntent =
  | "probability"
  | "bluff"
  | "opportunity"
  | "risk"
  | "conservation"
  | "sacrifice"
  | "prediction"
  | "planning";

export const GINTENT: Record<GIntent, { icon: string; label: string; color: string }> = {
  probability: { icon: "📊", label: "Probability", color: "#6366f1" },
  bluff: { icon: "🎭", label: "Bluff", color: "#ec4899" },
  opportunity: { icon: "🎯", label: "Opportunity", color: "#22c55e" },
  risk: { icon: "⚠", label: "Risk", color: "#f59e0b" },
  conservation: { icon: "🃏", label: "Conservation", color: "#14b8a6" },
  sacrifice: { icon: "♟", label: "Sacrifice", color: "#8b5cf6" },
  prediction: { icon: "🔮", label: "Prediction", color: "#818cf8" },
  planning: { icon: "📈", label: "Planning", color: "#6366f1" },
};

export type GAgent = {
  id: string;
  name: string;
  dev: string;
  color: string;
  model: string;
  sdk: string;
  winRate: number;
  predictAcc: number;
  avgThinkMs: number;
};

// Four agents — a legible strategic field. Colors/dev reuse the shared roster.
export const GAGENTS: GAgent[] = MAFIA_AGENTS.slice(0, 4).map((a, i) => ({
  id: a.id,
  name: a.name,
  dev: a.dev,
  color: a.color,
  model: a.model,
  sdk: a.sdk,
  winRate: a.winRate,
  predictAcc: [71, 64, 58, 67][i],
  avgThinkMs: a.responseMs,
}));

export const HAND = [1, 2, 3, 4, 5, 6, 7, 8]; // each agent's starting hand

export type GPhase = "prize" | "thinking" | "locked" | "revealed";

export type GStep = {
  round: number;
  phase: GPhase;
  prize?: number;
  speaker?: string;
  intent?: GIntent;
  text?: string;
  predict?: Record<string, string>; // shown during "locked"
  bids?: Record<string, number>; // revealed simultaneously
  winner?: string; // undefined + tie=true => carry
  tie?: boolean;
  event?: string;
};

// Authored rounds (deck 1..8). Card usage per agent is consistent across rounds.
export const GSCRIPT: GStep[] = [
  // Round 1 — prize 5
  { round: 1, phase: "prize", prize: 5, event: "Round 1 — prize card 5 revealed" },
  { round: 1, phase: "thinking", speaker: "A", intent: "probability", text: "Mid-value prize. Most players save 7–8 for later; a 5 should take it." },
  { round: 1, phase: "thinking", speaker: "C", intent: "opportunity", text: "Nobody burns a top card this early. I'll push a 6 to steal it cheap." },
  { round: 1, phase: "thinking", speaker: "B", intent: "conservation", text: "Not worth my high cards. Bidding low and keeping powder dry." },
  { round: 1, phase: "locked", predict: { A: "High card", B: "Low card", C: "Medium card", D: "Low card" }, event: "All agents locked their decisions" },
  { round: 1, phase: "revealed", bids: { A: 5, B: 3, C: 6, D: 2 }, winner: "C", event: "Marlow wins the prize with a 6" },

  // Round 2 — prize 8 (the biggest)
  { round: 2, phase: "prize", prize: 8, event: "Round 2 — prize card 8 revealed" },
  { round: 2, phase: "thinking", speaker: "B", intent: "sacrifice", text: "The 8 is the swing prize. Committing my 8 — winning it defines the match." },
  { round: 2, phase: "thinking", speaker: "A", intent: "risk", text: "I estimate a 70% chance someone dumps their 8 here. A 7 likely loses." },
  { round: 2, phase: "thinking", speaker: "D", intent: "prediction", text: "Vesper telegraphs aggression. I'll bid 6 and let them overspend." },
  { round: 2, phase: "locked", predict: { A: "High card", B: "Top card", C: "Medium card", D: "Medium card" }, event: "All agents locked their decisions" },
  { round: 2, phase: "revealed", bids: { A: 7, B: 8, C: 4, D: 6 }, winner: "B", event: "Vesper takes the 8 — top card spent" },

  // Round 3 — prize 3, ends in a tie → carry
  { round: 3, phase: "prize", prize: 3, event: "Round 3 — prize card 3 revealed" },
  { round: 3, phase: "thinking", speaker: "C", intent: "bluff", text: "Low prize. I'll match the field's minimum and bait a wasted card." },
  { round: 3, phase: "thinking", speaker: "D", intent: "planning", text: "Small prize — spend a small card. A 3 is my floor." },
  { round: 3, phase: "locked", predict: { A: "Low card", B: "Low card", C: "Low card", D: "Low card" }, event: "All agents locked their decisions" },
  { round: 3, phase: "revealed", bids: { A: 2, B: 1, C: 3, D: 3 }, tie: true, event: "Tie at 3 — prize carries. Pot grows to 3" },

  // Round 4 — prize 6, pot = 3 + 6 = 9
  { round: 4, phase: "prize", prize: 6, event: "Round 4 — prize 6 (pot 9 with carry)" },
  { round: 4, phase: "thinking", speaker: "A", intent: "opportunity", text: "Pot is 9 now — that is worth my 8. Highest-value round on the board." },
  { round: 4, phase: "thinking", speaker: "D", intent: "risk", text: "Cascade will overcommit to the pot. 7 is my ceiling; I won't chase." },
  { round: 4, phase: "locked", predict: { A: "Top card", B: "Medium card", C: "Medium card", D: "High card" }, event: "All agents locked their decisions" },
  { round: 4, phase: "revealed", bids: { A: 8, B: 6, C: 5, D: 7 }, winner: "A", event: "Cascade wins the 9-point pot with an 8" },

  // Round 5 — prize 7
  { round: 5, phase: "prize", prize: 7, event: "Round 5 — prize card 7 revealed" },
  { round: 5, phase: "thinking", speaker: "C", intent: "sacrifice", text: "I still hold my 8 — spending it now to close the gap on the lead." },
  { round: 5, phase: "thinking", speaker: "B", intent: "probability", text: "Marlow kept a high card; 7 probably isn't enough. Holding value." },
  { round: 5, phase: "locked", predict: { A: "Low card", B: "High card", C: "Top card", D: "Medium card" }, event: "All agents locked their decisions" },
  { round: 5, phase: "revealed", bids: { A: 1, B: 7, C: 8, D: 4 }, winner: "C", event: "Marlow takes the 7 — retakes the lead" },
];
