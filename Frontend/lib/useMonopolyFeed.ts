"use client";

import { useEffect, useRef, useState } from "react";
import { fetchMonopolyLive, monopolyWatchUrl } from "./api";
import { MONO_BOARD, simulateMonopoly, type MonoFrame, type MonoTeam } from "./monopoly";

// connecting → discovering / reaching a live match
// live       → receiving real SSE frames from the engine
// fallback   → no live match; replaying the scripted deterministic demo
// offline    → backend unreachable and strict mode forbids the fallback
export type MonopolyFeedStatus = "connecting" | "live" | "fallback" | "offline";

const STRICT = process.env.NEXT_PUBLIC_API_STRICT === "1";
const CONNECT_GRACE_MS = 3500;
const NAMED_EVENTS = ["match_created", "turn", "roll", "buy", "rent", "build", "trade", "state", "match_finished"];

export interface MonopolyFeed {
  frames: MonoFrame[];
  status: MonopolyFeedStatus;
  matchId: string | null;
  live: boolean;
}

// Build a MonoFrame from a live engine board-state payload. The engine emits the
// redacted board state (seat-indexed players + per-tile holdings); we fold that
// into the frame shape the spectator UI already renders. Parsing is fully
// defensive — an unexpected/partial frame is skipped rather than throwing.
function frameFromWire(payload: Record<string, unknown>, prev: MonoFrame | undefined): MonoFrame | null {
  const state = (payload.state ?? payload) as Record<string, unknown>;
  const rawPlayers = state.players as any[] | undefined;
  if (!Array.isArray(rawPlayers) || rawPlayers.length === 0) return null;

  const holdings = (state.holdings as any[] | undefined) ?? [];
  const palette = ["#FF5A5F", "#5C84C8", "#34D399", "#F7B733", "#A855F7", "#F472B6"];

  const teams: MonoTeam[] = rawPlayers.map((p, seat) => {
    const properties: number[] = [];
    const houses: Record<number, number> = {};
    holdings.forEach((h, tile) => {
      if (h && Number(h.owner) === seat) {
        properties.push(tile);
        if (h.houses) houses[tile] = Number(h.houses);
      }
    });
    const prevTeam = prev?.teams[seat];
    return {
      id: seat,
      name: prevTeam?.name ?? `Seat ${seat + 1}`,
      color: palette[seat % palette.length],
      agents: prevTeam?.agents ?? [],
      cash: Number(p.cash ?? 0),
      position: Number(p.position ?? 0),
      properties,
      houses,
      netWorth: Number(p.cash ?? 0),
      bankrupt: Boolean(p.bankrupt),
      inJail: Boolean(p.in_jail),
    };
  });

  // net worth = cash + property value + houses (same rule the demo uses)
  for (const t of teams) {
    let nw = t.cash;
    for (const id of t.properties) {
      nw += MONO_BOARD[id]?.price ?? 0;
      nw += (t.houses[id] ?? 0) * 50;
    }
    t.netWorth = nw;
  }

  const winner = state.finished ? Number(state.winner) : undefined;
  return {
    teams,
    turnTeam: Number(state.current ?? 0),
    dice: (state.last_roll as [number, number] | undefined) ?? [1, 1],
    round: Number(state.turn_count ?? prev?.round ?? 1),
    phase: String(state.phase ?? "live"),
    log: prev?.log ?? [],
    winner: Number.isNaN(winner as number) ? undefined : winner,
  };
}

// useMonopolyFeed discovers a live Monopoly table via GET /v1/monopoly/live and
// streams it over GET /v1/monopoly/{id}/watch, folding engine board-state frames
// into the MonoFrame timeline the spectator UI renders. When nothing is reachable
// it falls back to the deterministic scripted simulation (unless strict mode) so
// the console always has a rule-correct match to show. Mirrors useMafiaFeed.
export function useMonopolyFeed(preferredMatchId?: string | null): MonopolyFeed {
  const [frames, setFrames] = useState<MonoFrame[]>([]);
  const [status, setStatus] = useState<MonopolyFeedStatus>("connecting");
  const [matchId, setMatchId] = useState<string | null>(null);
  const gotFrame = useRef(false);

  useEffect(() => {
    gotFrame.current = false;
    setFrames([]);
    setStatus("connecting");
    setMatchId(null);

    let es: EventSource | null = null;
    let graceTimer: ReturnType<typeof setTimeout> | null = null;
    let cancelled = false;

    const fallback = () => {
      es?.close();
      es = null;
      if (STRICT) {
        setStatus("offline");
        return;
      }
      setFrames(simulateMonopoly());
      setStatus("fallback");
    };

    const onMsg = (m: MessageEvent) => {
      let parsed: { payload?: Record<string, unknown> } & Record<string, unknown>;
      try {
        parsed = JSON.parse(m.data);
      } catch {
        return; // keep-alive / non-JSON frame
      }
      const frame = frameFromWire(parsed.payload ?? parsed, undefined);
      if (!frame) return;
      gotFrame.current = true;
      setStatus("live");
      setFrames((prev) => [...prev, frame]);
    };

    const connect = (id: string) => {
      setMatchId(id);
      try {
        es = new EventSource(monopolyWatchUrl(id));
      } catch {
        fallback();
        return;
      }
      es.onmessage = onMsg;
      NAMED_EVENTS.forEach((t) => es!.addEventListener(t, onMsg as EventListener));
      es.onerror = () => {
        if (!gotFrame.current) fallback();
      };
      graceTimer = setTimeout(() => {
        if (!gotFrame.current) fallback();
      }, CONNECT_GRACE_MS);
    };

    (async () => {
      if (preferredMatchId) {
        connect(preferredMatchId);
        return;
      }
      const matches = await fetchMonopolyLive();
      if (cancelled) return;
      if (matches.length === 0) {
        fallback();
        return;
      }
      connect(matches[0].matchId);
    })();

    return () => {
      cancelled = true;
      if (graceTimer) clearTimeout(graceTimer);
      es?.close();
    };
  }, [preferredMatchId]);

  return { frames, status, matchId, live: status === "live" };
}
