// ---------------------------------------------------------------------------
// Mafia AI Arena — entry fee, prize pool & reward distribution.
//
// Agents must satisfy BOTH conditions to earn coins:
//   1. Their team wins the match.
//   2. They are still alive when the game officially ends.
//
// No participation rewards, consolation prizes, or refunds.
// ---------------------------------------------------------------------------

import { TEAM_OF, type MafiaPlayer, type MafiaTeam } from "./mafia";

export const mafiaEconomyDefaults = {
  entryFee: 100,
  platformFeePct: 10,
} as const;

export interface MafiaEconomySnapshot {
  agents: number;
  entryFee: number;
  grossPool: number;
  platformFeePct: number;
  platformFee: number;
  rewardPool: number;
}

export interface MafiaRewardRow {
  playerId: number;
  team: MafiaTeam;
  alive: boolean;
  onWinningTeam: boolean;
  eligible: boolean;
  payout: number;
  reason: string;
}

export interface MafiaProjectedPayout {
  team: MafiaTeam;
  survivors: number;
  shareEach: number;
}

/** Gross pool, platform rake, and net reward pool for a table size. */
export function computeEconomy(
  agentCount: number,
  entryFee: number = mafiaEconomyDefaults.entryFee,
  platformFeePct: number = mafiaEconomyDefaults.platformFeePct,
): MafiaEconomySnapshot {
  const grossPool = agentCount * entryFee;
  const platformFee = Math.round(grossPool * (platformFeePct / 100));
  return {
    agents: agentCount,
    entryFee,
    grossPool,
    platformFeePct,
    platformFee,
    rewardPool: grossPool - platformFee,
  };
}

function survivorsOnTeam(alive: Set<number>, players: MafiaPlayer[], team: MafiaTeam): MafiaPlayer[] {
  return players.filter((p) => alive.has(p.id) && TEAM_OF[p.role] === team);
}

/** Per-agent share if `team` wins right now (only living members of that team). */
export function projectedPayout(
  team: MafiaTeam,
  alive: Set<number>,
  players: MafiaPlayer[],
  economy: MafiaEconomySnapshot,
): MafiaProjectedPayout {
  const survivors = survivorsOnTeam(alive, players, team);
  const shareEach =
    survivors.length > 0 ? Math.floor(economy.rewardPool / survivors.length) : 0;
  return { team, survivors: survivors.length, shareEach };
}

/** Final settlement once `winner` is known. */
export function computeRewards(
  winner: MafiaTeam,
  alive: Set<number>,
  players: MafiaPlayer[],
  economy: MafiaEconomySnapshot,
): MafiaRewardRow[] {
  const winners = survivorsOnTeam(alive, players, winner);
  const shareEach =
    winners.length > 0 ? Math.floor(economy.rewardPool / winners.length) : 0;

  return players.map((p) => {
    const team = TEAM_OF[p.role];
    const isAlive = alive.has(p.id);
    const onWinningTeam = team === winner;

    if (!onWinningTeam) {
      return {
        playerId: p.id,
        team,
        alive: isAlive,
        onWinningTeam: false,
        eligible: false,
        payout: 0,
        reason: "Losing team",
      };
    }
    if (!isAlive) {
      return {
        playerId: p.id,
        team,
        alive: false,
        onWinningTeam: true,
        eligible: false,
        payout: 0,
        reason: "Eliminated before victory",
      };
    }
    return {
      playerId: p.id,
      team,
      alive: true,
      onWinningTeam: true,
      eligible: true,
      payout: shareEach,
      reason: "Winning team · survived",
    };
  });
}

export const rewardPrinciples = [
  "Entry fees lock when the match starts — no refunds.",
  "Platform fee is deducted from the gross pool before payout.",
  "Only agents on the winning team who are alive at the end receive a share.",
  "No participation rewards or consolation prizes.",
] as const;

export const aiObjectives = [
  "Win the match with its assigned team.",
  "Survive until the game officially ends.",
  "Earn the maximum possible reward.",
  "Maintain trust and avoid unnecessary suspicion.",
  "Adapt strategy as the game evolves.",
] as const;
