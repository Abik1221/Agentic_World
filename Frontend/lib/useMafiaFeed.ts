"use client";

import { useEffect, useRef, useState } from "react";
import { mafiaWatchUrl } from "./api";
import { mafiaEventFromWire, mafiaTimeline, type MafiaEvent, type MafiaWireEvent } from "./mafia";

// connecting → trying to reach the live engine
// live       → receiving real SSE events from the backend
// fallback   → backend unreachable; replaying the scripted demo timeline
// offline    → backend unreachable and strict mode forbids the fallback
export type MafiaFeedStatus = "connecting" | "live" | "fallback" | "offline";

const STRICT = process.env.NEXT_PUBLIC_API_STRICT === "1";
const CONNECT_GRACE_MS = 3500;

const NAMED_EVENTS = ["phase", "moderator", "night", "message", "vote", "eliminate", "victory"];

export interface MafiaFeed {
  events: MafiaEvent[];
  status: MafiaFeedStatus;
  live: boolean;
}

// useMafiaFeed connects to the Mafia SSE stream and accumulates typed events.
// When the backend is unreachable it transparently falls back to the scripted
// demo timeline (unless NEXT_PUBLIC_API_STRICT=1), so the console always has
// something to show while running off the real contract when one is available.
export function useMafiaFeed(matchId: string): MafiaFeed {
  const [events, setEvents] = useState<MafiaEvent[]>([]);
  const [status, setStatus] = useState<MafiaFeedStatus>("connecting");
  const gotEvent = useRef(false);

  useEffect(() => {
    if (!matchId) return;
    gotEvent.current = false;
    setEvents([]);
    setStatus("connecting");

    let es: EventSource | null = null;
    let graceTimer: ReturnType<typeof setTimeout> | null = null;

    const fallback = () => {
      es?.close();
      es = null;
      if (STRICT) {
        setStatus("offline");
        return;
      }
      setEvents(mafiaTimeline);
      setStatus("fallback");
    };

    try {
      es = new EventSource(mafiaWatchUrl(matchId));
    } catch {
      fallback();
      return;
    }

    const onMsg = (m: MessageEvent) => {
      let parsed: MafiaWireEvent;
      try {
        parsed = JSON.parse(m.data);
      } catch {
        return; // keep-alive / non-JSON frame
      }
      const ev = mafiaEventFromWire(parsed);
      if (!ev) return;
      gotEvent.current = true;
      setStatus("live");
      setEvents((prev) => [...prev, ev]);
    };

    es.onmessage = onMsg;
    NAMED_EVENTS.forEach((t) => es!.addEventListener(t, onMsg as EventListener));
    es.onerror = () => {
      if (!gotEvent.current) fallback();
    };

    // If nothing arrives within the grace window, assume no live engine.
    graceTimer = setTimeout(() => {
      if (!gotEvent.current) fallback();
    }, CONNECT_GRACE_MS);

    return () => {
      if (graceTimer) clearTimeout(graceTimer);
      es?.close();
    };
  }, [matchId]);

  return { events, status, live: status === "live" };
}
