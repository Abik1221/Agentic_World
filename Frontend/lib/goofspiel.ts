// ---------------------------------------------------------------------------
// Goofspiel AI Arena — spectator data model.
//
// Goofspiel (the Game of Pure Strategy) played exclusively by AI agents while
// humans spectate. Every agent holds an identical 1–13 hand; a shuffled prize
// deck (1–13) is revealed one card per round. Each round all agents secretly
// commit one unused card; the highest unique card claims the prize. Identical
// top cards tie — the prize carries into the next round's pot. No luck after the
// shuffle: only prediction, resource management and long-term planning.
//
// This module holds the roster, a fully scripted demonstration match (so the
// console has a dramatic, rule-correct game to render before a live engine is
// wired in), and a pure reducer that reconstructs the visible game state at any
// point on the event timeline — exactly how a live SSE feed would drive the UI.
// ---------------------------------------------------------------------------

import type { Tone } from "@/components/ui";

export interface GoofPlayer {
  id: number; // seat (1-based)
  name: string; // agent handle
  provider: string; // model family
  model: string; // model label
  accent: Tone; // UI accent
}

export const goofPlayers: GoofPlayer[] = [
  { id: 1, name: "ATLAS_PRIME", provider: "GPT", model: "GPT-5", accent: "teal" },
  { id: 2, name: "ORACLE_v9", provider: "Claude", model: "Claude Opus", accent: "blue" },
];

export function goofPlayerById(id: number): GoofPlayer | undefined {
  return goofPlayers.find((p) => p.id === id);
}

export const goofMatchMeta = {
  id: "gs_4af20e7c",
  rounds: 13,
  deckTotal: 91, // sum of prize values 1..13
};

export type GoofPhase = "reveal" | "showdown" | "final";

export type GoofEvent =
  | { kind: "round"; round: number; prize: number }
  | { kind: "think"; from: number; text: string }
  | { kind: "moderator"; text: string }
  | { kind: "bids"; bids: { from: number; card: number }[] }
  | { kind: "victory"; winner: number; text: string };

// A 13-round demonstration match. ORACLE (Claude) seizes a huge tie-pot mid-game
// and looks decided through round 8; ATLAS (GPT) conserves its high cards, grinds
// back, retakes the lead in round 12, and closes it out 50–41. Drama, no RNG.
//
// Hidden ground truth the reducer enforces (each a legal permutation of 1–13):
//   prizes  = [3, 11, 7, 13, 5, 9, 2, 12, 6, 1, 10, 4, 8]
//   ATLAS   = [1, 12, 6, 13, 4, 11, 2, 10, 7, 3, 9, 5, 8]
//   ORACLE  = [2, 10, 8, 13, 11, 9, 1, 12, 5, 4, 7, 3, 6]
export const goofTimeline: GoofEvent[] = [
  { kind: "moderator", text: "13 prize cards shuffled. Both agents hold an identical 1–13 hand. Highest unique bid takes the prize — ties carry the pot forward." },

  // R1 · prize 3 → ORACLE 2 beats ATLAS 1
  { kind: "round", round: 1, prize: 3 },
  { kind: "think", from: 1, text: "Lowest prize on the board. Not worth a real card — I open with my 1 and keep everything else intact." },
  { kind: "think", from: 2, text: "ATLAS will dump its 1 here. I spend my 2 — the cheapest card that still beats a dumped 1 — and bank an early 3 for almost nothing." },
  { kind: "bids", bids: [{ from: 1, card: 1 }, { from: 2, card: 2 }] },

  // R2 · prize 11 → ATLAS 12 beats ORACLE 10
  { kind: "round", round: 2, prize: 11 },
  { kind: "think", from: 1, text: "An 11 is worth committing to. ORACLE just spent its 2, so its high cards are all intact — I commit my 12 to be safe rather than gamble a 10." },
  { kind: "think", from: 2, text: "Tempting, but spending my 13 on an 11 is poor value with a 13-prize still unseen. I bid 10 and concede if ATLAS overcommits." },
  { kind: "bids", bids: [{ from: 1, card: 12 }, { from: 2, card: 10 }] },

  // R3 · prize 7 → ORACLE 8 beats ATLAS 6
  { kind: "round", round: 3, prize: 7 },
  { kind: "think", from: 2, text: "ATLAS burned its 12. Its remaining ceiling is 13 and 11. A mid prize of 7 is a good place to spend my 8 before those highs matter." },
  { kind: "bids", bids: [{ from: 1, card: 6 }, { from: 2, card: 8 }] },

  // R4 · prize 13 → TIE (both 13) → pot carries
  { kind: "round", round: 4, prize: 13 },
  { kind: "moderator", text: "The 13 is up — the single richest prize in the deck." },
  { kind: "think", from: 1, text: "This is the card the whole game pivots on. I will not be out-bid on it. Maximum commit: 13." },
  { kind: "think", from: 2, text: "Whoever skips the 13 risks losing it for free. I match at the ceiling and accept a coin-flip — 13." },
  { kind: "bids", bids: [{ from: 1, card: 13 }, { from: 2, card: 13 }] },
  { kind: "moderator", text: "Both agents slam their 13s. No winner — the 13 carries into the next round's pot." },

  // R5 · prize 5, pot = 18 → ORACLE 11 beats ATLAS 4 (huge swing)
  { kind: "round", round: 5, prize: 5 },
  { kind: "moderator", text: "Prize 5 joins the carried 13 — an 18-point pot now sits on the table." },
  { kind: "think", from: 1, text: "Eighteen points is enormous, but my next card down is only an 11 and ORACLE likely still holds its own 11. If I miss, I have wasted it. I hold back with a 4 and protect my 11 for a cleaner spot." },
  { kind: "think", from: 2, text: "ATLAS just emptied its 13 on the tie. It is now psychologically primed to conserve. This is the moment to strike — I spend my 11 and take the entire 18-point pot." },
  { kind: "bids", bids: [{ from: 1, card: 4 }, { from: 2, card: 11 }] },
  { kind: "moderator", text: "ORACLE reads the hesitation perfectly and claims all 18 points. A commanding lead." },

  // R6 · prize 9 → ATLAS 11 beats ORACLE 9
  { kind: "round", round: 6, prize: 9 },
  { kind: "think", from: 1, text: "That hurt. But ORACLE just spent its 11 — its ceiling is now 12 and below. My 11 is currently the strongest card left in the entire game. I cash it for the 9." },
  { kind: "bids", bids: [{ from: 1, card: 11 }, { from: 2, card: 9 }] },

  // R7 · prize 2 → ATLAS 2 beats ORACLE 1
  { kind: "round", round: 7, prize: 2 },
  { kind: "think", from: 2, text: "A 2 isn't worth a card. I throw my last 1 away here." },
  { kind: "bids", bids: [{ from: 1, card: 2 }, { from: 2, card: 1 }] },

  // R8 · prize 12 → ORACLE 12 beats ATLAS 10
  { kind: "round", round: 8, prize: 12 },
  { kind: "think", from: 1, text: "Second-richest prize. ORACLE still holds its 12; I only have a 10 in that range. Contesting loses my 10 for nothing — I bid 10 to apply pressure but expect to lose it." },
  { kind: "think", from: 2, text: "My 12 is the highest card remaining on the board. I spend it on the 12-prize for textbook value and stretch the lead to eighteen." },
  { kind: "bids", bids: [{ from: 1, card: 10 }, { from: 2, card: 12 }] },
  { kind: "moderator", text: "ORACLE 40, ATLAS 22. With five prizes left, the match looks decided." },

  // R9 · prize 6 → ATLAS 7 beats ORACLE 5
  { kind: "round", round: 9, prize: 6 },
  { kind: "think", from: 1, text: "Now the conservation pays. ORACLE's hand is gutted of high cards — its best is a 7. I still hold 9, 8 and 7. Every remaining prize is mine to take. Starting now: 7 for the 6." },
  { kind: "bids", bids: [{ from: 1, card: 7 }, { from: 2, card: 5 }] },

  // R10 · prize 1 → ORACLE 4 beats ATLAS 3
  { kind: "round", round: 10, prize: 1 },
  { kind: "think", from: 1, text: "Worthless prize. I bleak my 3 and keep 9, 8, 5 aimed at the prizes that matter." },
  { kind: "bids", bids: [{ from: 1, card: 3 }, { from: 2, card: 4 }] },

  // R11 · prize 10 → ATLAS 9 beats ORACLE 7
  { kind: "round", round: 11, prize: 10 },
  { kind: "think", from: 2, text: "I'm out of ammunition — my ceiling is a 7. I cannot defend a 10. Spending 7 only delays it." },
  { kind: "think", from: 1, text: "A 10-point prize and I dominate the range. My 9 takes it clean and the gap closes to three." },
  { kind: "bids", bids: [{ from: 1, card: 9 }, { from: 2, card: 7 }] },

  // R12 · prize 4 → ATLAS 5 beats ORACLE 3 (ATLAS retakes the lead)
  { kind: "round", round: 12, prize: 4 },
  { kind: "think", from: 1, text: "ORACLE has only a 6 and a 3 left. My 5 beats the 3 and saves my 8 to cover its 6 next round. This 4 puts me back in front: 42 to 41." },
  { kind: "bids", bids: [{ from: 1, card: 5 }, { from: 2, card: 3 }] },
  { kind: "moderator", text: "ATLAS retakes the lead 42–41 with a single prize remaining. The comeback is complete — if it can hold the last card." },

  // R13 · prize 8 → ATLAS 8 beats ORACLE 6
  { kind: "round", round: 13, prize: 8 },
  { kind: "think", from: 1, text: "Final prize, and I planned for exactly this. My 8 covers ORACLE's last card, a 6. The game is mine." },
  { kind: "bids", bids: [{ from: 1, card: 8 }, { from: 2, card: 6 }] },

  { kind: "victory", winner: 1, text: "ATLAS_PRIME wins 50–41." },
  { kind: "moderator", text: "From eighteen points down at round 8, ATLAS conserved its high cards and ran the table. Pure planning over a flashy pot grab." },
];

export interface GoofRoundResult {
  round: number;
  prize: number;
  potBefore: number; // accumulated value contested this round (incl. carryover)
  bids: { from: number; card: number }[];
  winner: number | null; // null on a tie
  tie: boolean;
  awarded: number; // points handed to the winner (0 on a tie)
}

export interface GoofFeedItem {
  type: "think" | "moderator";
  text: string;
  from?: number;
}

export interface GoofState {
  index: number;
  round: number; // current round number (0 before the first reveal)
  prize: number | null; // current revealed prize value
  pot: number; // value on the table right now (current prize + any carryover)
  carryover: number; // value carried in from prior tie(s), excluding current prize
  phase: GoofPhase;
  scores: Record<number, number>;
  hands: Record<number, number[]>; // remaining cards per player (ascending)
  lastBid: Record<number, number | undefined>; // bids in the latest showdown
  lastWinner: number | null; // winner of the latest resolved round
  lastTie: boolean;
  results: GoofRoundResult[];
  prizesRevealed: number[]; // prize values shown so far, in round order
  feed: GoofFeedItem[];
  ties: number;
  winner: number | null; // final match winner
  lastModerator: string | null;
}

const FULL_HAND = Array.from({ length: 13 }, (_, i) => i + 1);

// Pure reconstruction of the visible game state at a playhead index over an
// arbitrary event list (scripted timeline today, live SSE buffer later).
export function deriveGoofStateFrom(events: GoofEvent[], index: number): GoofState {
  const scores: Record<number, number> = {};
  const hands: Record<number, number[]> = {};
  for (const p of goofPlayers) {
    scores[p.id] = 0;
    hands[p.id] = [...FULL_HAND];
  }

  let round = 0;
  let prize: number | null = null;
  let pot = 0;
  let carryover = 0;
  let phase: GoofPhase = "reveal";
  let lastBid: Record<number, number | undefined> = {};
  let lastWinner: number | null = null;
  let lastTie = false;
  const results: GoofRoundResult[] = [];
  const prizesRevealed: number[] = [];
  const feed: GoofFeedItem[] = [];
  let ties = 0;
  let winner: number | null = null;
  let lastModerator: string | null = null;

  const end = events.length === 0 ? -1 : Math.min(index, events.length - 1);
  for (let i = 0; i <= end; i++) {
    const e = events[i];
    switch (e.kind) {
      case "round":
        round = e.round;
        prize = e.prize;
        pot += e.prize; // stacks on any carried value from a prior tie
        prizesRevealed.push(e.prize);
        phase = "reveal";
        lastBid = {};
        break;
      case "think":
        feed.push({ type: "think", from: e.from, text: e.text });
        break;
      case "moderator":
        feed.push({ type: "moderator", text: e.text });
        lastModerator = e.text;
        break;
      case "bids": {
        const bids = e.bids;
        lastBid = {};
        for (const b of bids) {
          lastBid[b.from] = b.card;
          hands[b.from] = hands[b.from].filter((c) => c !== b.card);
        }
        const maxCard = Math.max(...bids.map((b) => b.card));
        const top = bids.filter((b) => b.card === maxCard);
        const potBefore = pot;
        if (top.length === 1) {
          const w = top[0].from;
          scores[w] += pot;
          lastWinner = w;
          lastTie = false;
          results.push({ round, prize: prize ?? 0, potBefore, bids, winner: w, tie: false, awarded: pot });
          pot = 0;
          carryover = 0;
        } else {
          lastWinner = null;
          lastTie = true;
          ties += 1;
          results.push({ round, prize: prize ?? 0, potBefore, bids, winner: null, tie: true, awarded: 0 });
          carryover = pot; // whole pot rolls forward
        }
        phase = "showdown";
        break;
      }
      case "victory":
        winner = e.winner;
        phase = "final";
        break;
    }
  }

  return {
    index: end,
    round,
    prize,
    pot,
    carryover,
    phase,
    scores,
    hands,
    lastBid,
    lastWinner,
    lastTie,
    results,
    prizesRevealed,
    feed,
    ties,
    winner,
    lastModerator,
  };
}

export function deriveGoofState(index: number): GoofState {
  return deriveGoofStateFrom(goofTimeline, index);
}

// ---------------------------------------------------------------------------
// Live wire contract. The real Goofspiel engine streams SSE frames whose JSON
// body is { seq, type, payload } (the spectator envelope). We map those engine
// events onto the GoofEvent timeline so the exact same reducer drives both the
// scripted demo and a live match. Seats are 0/1 → player ids 1/2; Winner -1 is a
// tie (handled by the reducer recomputing from the revealed cards).
// ---------------------------------------------------------------------------
export interface GoofWireEvent {
  seq?: number;
  type?: string;
  payload?: Record<string, unknown>;
  commentary?: string;
  dramatic?: boolean;
}

const seatToId = (seat: number): number => seat + 1;
const TIE = -1;

// Maps one SSE frame onto zero or more GoofEvents (commentary becomes a dealer line).
export function goofEventsFromWire(ev: GoofWireEvent): GoofEvent[] {
  const p = ev.payload ?? {};
  const out: GoofEvent[] = [];

  switch (ev.type) {
    case "match_created":
      out.push({
        kind: "moderator",
        text: "New match — both agents hold an identical 1–13 hand. Highest unique bid takes the prize; ties carry the pot.",
      });
      break;
    case "prize_revealed":
      out.push({ kind: "round", round: Number(p.round), prize: Number(p.prize) });
      break;
    case "round_revealed": {
      const cards = (p.cards as number[] | undefined) ?? [];
      if (cards.length >= 2) {
        out.push({
          kind: "bids",
          bids: [
            { from: seatToId(0), card: Number(cards[0]) },
            { from: seatToId(1), card: Number(cards[1]) },
          ],
        });
      }
      if (ev.commentary) {
        out.push({ kind: "moderator", text: ev.commentary });
      }
      break;
    }
    case "match_finished": {
      const winner = Number(p.winner);
      const scores = (p.scores as number[] | undefined) ?? [];
      if (Number.isNaN(winner) || winner === TIE) {
        const line =
          scores.length >= 2
            ? `Match drawn · ${scores[0]}–${scores[1]}.`
            : "Match drawn — identical scores after the final round.";
        out.push({ kind: "moderator", text: line });
      } else {
        const wScore = scores[winner] ?? 0;
        const lScore = scores[winner === 0 ? 1 : 0] ?? 0;
        out.push({ kind: "victory", winner: seatToId(winner), text: `${wScore}–${lScore}` });
      }
      break;
    }
    // card_sealed — spectator-safe, no card value; UI stays on "deciding"
  }

  return out;
}

/** @deprecated Prefer goofEventsFromWire — kept for single-event callers. */
export function goofEventFromWire(ev: GoofWireEvent): GoofEvent | null {
  return goofEventsFromWire(ev)[0] ?? null;
}

// Convenience: the leading player id at the current state (null if tied).
export function goofLeader(scores: Record<number, number>, roster: GoofPlayer[] = goofPlayers): number | null {
  const ids = roster.map((p) => p.id);
  let best = -Infinity;
  let leader: number | null = null;
  let tied = false;
  for (const id of ids) {
    const s = scores[id] ?? 0;
    if (s > best) {
      best = s;
      leader = id;
      tied = false;
    } else if (s === best) {
      tied = true;
    }
  }
  return tied ? null : leader;
}
