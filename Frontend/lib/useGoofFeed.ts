"use client";

import { useEffect, useRef, useState } from "react";
import { fetchLiveMatchesRaw, watchUrl } from "./api";
import {
  goofEventsFromWire,
  goofTimeline,
  goofPlayers,
  type GoofEvent,
  type GoofPlayer,
  type GoofWireEvent,
} from "./goofspiel";

// connecting → discovering / reaching a live match
// live       → receiving real SSE events from the engine
// fallback   → no live match; replaying the scripted demo
// offline    → backend unreachable and strict mode forbids the fallback
export type GoofFeedStatus = "connecting" | "live" | "fallback" | "offline";

const STRICT = process.env.NEXT_PUBLIC_API_STRICT === "1";
const CONNECT_GRACE_MS = 3500;
const NAMED_EVENTS = ["match_created", "prize_revealed", "card_sealed", "round_revealed", "match_finished"];

export interface GoofFeed {
  events: GoofEvent[];
  players: GoofPlayer[];
  status: GoofFeedStatus;
  matchId: string | null;
  live: boolean;
}

// Builds the 2-player roster from a live match's agent handles (seat 0 → id 1).
function playersFromAgents(agents: string[]): GoofPlayer[] {
  return goofPlayers.map((base, i) => ({
    ...base,
    name: agents[i] || base.name,
    provider: "Agent",
    model: "live",
  }));
}

// useGoofFeed streams a Goofspiel match over GET /v1/match/{id}/watch, mapping
// engine SSE frames onto the GoofEvent timeline. Pass preferredMatchId (e.g. from
// ?match=) to watch a specific table; otherwise the first live match is picked.
// When nothing is reachable it falls back to the scripted demo (unless strict mode).
export function useGoofFeed(preferredMatchId?: string | null): GoofFeed {
  const [events, setEvents] = useState<GoofEvent[]>([]);
  const [players, setPlayers] = useState<GoofPlayer[]>(goofPlayers);
  const [status, setStatus] = useState<GoofFeedStatus>("connecting");
  const [matchId, setMatchId] = useState<string | null>(null);
  const gotEvent = useRef(false);

  useEffect(() => {
    gotEvent.current = false;
    setEvents([]);
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
      setPlayers(goofPlayers);
      setEvents(goofTimeline);
      setStatus("fallback");
    };

    const appendWire = (parsed: GoofWireEvent) => {
      const mapped = goofEventsFromWire(parsed);
      if (mapped.length === 0) return;
      gotEvent.current = true;
      setStatus("live");
      setEvents((prev) => [...prev, ...mapped]);
    };

    const onMsg = (m: MessageEvent) => {
      try {
        appendWire(JSON.parse(m.data));
      } catch {
        /* keep-alive / non-JSON frame */
      }
    };

    const connect = (id: string, agents: string[]) => {
      setMatchId(id);
      if (agents.length > 0) setPlayers(playersFromAgents(agents));
      try {
        es = new EventSource(watchUrl(id));
      } catch {
        fallback();
        return;
      }
      es.onmessage = onMsg;
      NAMED_EVENTS.forEach((t) => es!.addEventListener(t, onMsg as EventListener));
      es.onerror = () => {
        if (!gotEvent.current) fallback();
      };
      graceTimer = setTimeout(() => {
        if (!gotEvent.current) fallback();
      }, CONNECT_GRACE_MS);
    };

    (async () => {
      if (preferredMatchId) {
        // Direct watch — works for live matches and finished replays (backlog).
        const matches = await fetchLiveMatchesRaw();
        if (cancelled) return;
        const meta = matches.find((m) => m.matchId === preferredMatchId);
        connect(preferredMatchId, meta?.agents ?? []);
        return;
      }

      const matches = await fetchLiveMatchesRaw();
      if (cancelled) return;
      if (matches.length === 0) {
        fallback();
        return;
      }
      connect(matches[0].matchId, matches[0].agents);
    })();

    return () => {
      cancelled = true;
      if (graceTimer) clearTimeout(graceTimer);
      es?.close();
    };
  }, [preferredMatchId]);

  return { events, players, status, matchId, live: status === "live" };
}
