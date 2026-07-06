"use client";

// ---------------------------------------------------------------------------
// useGoofspielLiveScript — feeds the scripted GoofspielViewer with LIVE data.
//
// The viewer (components/goofspiel/GoofspielViewer.tsx) is built around three
// demo imports: GAGENTS (the cast), GSCRIPT (an ordered list of GStep beats)
// and HAND (each agent's starting deck). This hook returns the SAME shapes
// ({ agents, script, hand }) so the viewer can render a real streaming match
// with zero changes to its animation/JSX — it just reads its data from here
// instead of the module-level demo constants.
//
// Live path: discover a goofspiel match (fetchLiveMatchesRaw, preferring one
// still in progress), open the SSE stream (watchUrl + EventSource), parse
// frames into the GoofEvent union (goofEventsFromWire), then fold those
// seat-based events into the viewer's agent-id-based GStep[]. A synthesized
// 2-agent cast (ids "1"/"2", matching goofspiel.ts seatToId = seat+1) backs
// the live seats.
//
// Demo is the HARD DEFAULT. When there is no live match, any error, or the
// backend is offline, the hook returns the demo GAGENTS / GSCRIPT / HAND
// unchanged (live:false) so the viewer renders exactly as it does today.
// ---------------------------------------------------------------------------

import { useEffect, useMemo, useRef, useState } from "react";
import { fetchLiveMatchesRaw, watchUrl } from "./api";
import { goofEventsFromWire, type GoofEvent, type GoofWireEvent } from "./goofspiel";
import {
  GAGENTS,
  GSCRIPT,
  HAND,
  type GAgent,
  type GPhase,
  type GStep,
} from "./goofspiel-demo";

const CONNECT_GRACE_MS = 3500;
const NAMED_EVENTS = ["match_created", "prize_revealed", "card_sealed", "round_revealed", "match_finished"];

// Live goofspiel is always a 2-player duel over a 1..13 hand.
const LIVE_HAND: number[] = Array.from({ length: 13 }, (_, i) => i + 1);

// A neutral per-seat palette for the synthetic live cast.
const SEAT_PALETTE = ["#6366f1", "#f59e0b"];

// The live engine seats 2 agents. Ids are the string form of goofspiel.ts's
// seatToId (seat 0 → id "1", seat 1 → id "2") so the mapped GStep bids/winner
// ids line up with the cast. Every GAgent field is filled with a neutral
// placeholder — real ratings aren't surfaced on the live spectator stream.
function buildLiveAgents(handles: string[]): GAgent[] {
  return [0, 1].map((seat) => ({
    id: String(seat + 1),
    name: handles[seat] || `Seat ${seat + 1}`,
    dev: "live",
    color: SEAT_PALETTE[seat % SEAT_PALETTE.length],
    model: "live",
    sdk: "—",
    winRate: 0,
    predictAcc: 0,
    avgThinkMs: 0,
  }));
}

// Fold the seat-based live event stream into the viewer's GStep[] beats,
// tracking the current round/phase so moderator lines carry the right context.
// think events are dropped (a live match surfaces no per-move reasoning), and
// the "thinking"/"locked" phases simply never occur on the live path.
function mapEventsToScript(events: GoofEvent[]): GStep[] {
  const steps: GStep[] = [];
  let round = 0;
  let phase: GPhase = "prize";

  for (const e of events) {
    switch (e.kind) {
      case "round":
        round = e.round;
        phase = "prize";
        steps.push({ round, phase: "prize", prize: e.prize, event: `Round ${e.round} — prize card ${e.prize} revealed` });
        break;
      case "bids": {
        const bids: Record<string, number> = {};
        for (const b of e.bids) bids[String(b.from)] = b.card;
        const cards = e.bids.map((b) => b.card);
        const maxCard = Math.max(...cards);
        const top = e.bids.filter((b) => b.card === maxCard);
        const tie = top.length !== 1;
        const winner = tie ? undefined : String(top[0].from);
        phase = "revealed";
        steps.push({
          round,
          phase: "revealed",
          bids,
          winner,
          tie,
          event: tie ? `Tie at ${maxCard} — prize carries` : `Round ${round} taken with a ${maxCard}`,
        });
        break;
      }
      case "moderator":
        steps.push({ round, phase, event: e.text });
        break;
      case "victory":
        phase = "revealed";
        steps.push({ round, phase: "revealed", winner: String(e.winner), event: e.text });
        break;
      case "think":
        break; // no live source for per-move reasoning — skip
    }
  }

  return steps;
}

export interface GoofspielLiveScript {
  agents: GAgent[];
  script: GStep[];
  hand: number[];
  live: boolean;
}

// useGoofspielLiveScript returns the cast + script + hand the GoofspielViewer
// should render. It streams a live match when one is available and maps it into
// the viewer's own shapes; otherwise it returns the scripted demo unchanged.
export function useGoofspielLiveScript(preferredMatchId?: string): GoofspielLiveScript {
  const [events, setEvents] = useState<GoofEvent[]>([]);
  const [handles, setHandles] = useState<string[]>([]);
  const [live, setLive] = useState(false);
  const gotEvent = useRef(false);

  useEffect(() => {
    // Spectating is a public SSE stream with no auth to surface, so we always
    // attempt to find and stream a live match; when none is reachable we simply
    // fall back to the demo script below.
    let cancelled = false;
    let es: EventSource | null = null;
    let graceTimer: ReturnType<typeof setTimeout> | null = null;
    const ctrl = new AbortController();

    gotEvent.current = false;
    setEvents([]);
    setHandles([]);
    setLive(false);

    const connect = (id: string) => {
      try {
        es = new EventSource(watchUrl(id));
      } catch {
        return; // demo default stays in place
      }

      const onMsg = (m: MessageEvent) => {
        let parsed: GoofWireEvent;
        try {
          parsed = JSON.parse(m.data);
        } catch {
          return; // keep-alive / non-JSON frame
        }
        const mapped = goofEventsFromWire(parsed);
        if (mapped.length === 0) return;
        gotEvent.current = true;
        setLive(true);
        setEvents((prev) => [...prev, ...mapped]);
      };

      es.onmessage = onMsg;
      NAMED_EVENTS.forEach((t) => es!.addEventListener(t, onMsg as EventListener));
      es.onerror = () => {
        // If the stream drops before any event arrives, stay on the demo.
        if (!gotEvent.current) {
          es?.close();
          es = null;
        }
      };

      // If nothing arrives within the grace window, assume no live engine.
      graceTimer = setTimeout(() => {
        if (!gotEvent.current) {
          es?.close();
          es = null;
        }
      }, CONNECT_GRACE_MS);
    };

    (async () => {
      const matches = await fetchLiveMatchesRaw(ctrl.signal);
      if (cancelled) return;
      let pick = preferredMatchId ? matches.find((m) => m.matchId === preferredMatchId) : undefined;
      // Prefer a match still in progress (round < totalRounds) over a finished one.
      if (!pick) pick = matches.find((m) => m.round < m.totalRounds) ?? matches[0];
      const id = preferredMatchId ?? pick?.matchId;
      if (!id) return;
      if (pick?.agents?.length) setHandles(pick.agents);
      connect(id);
    })();

    return () => {
      cancelled = true;
      ctrl.abort();
      if (graceTimer) clearTimeout(graceTimer);
      es?.close();
    };
  }, [preferredMatchId]);

  const liveAgents = useMemo(() => buildLiveAgents(handles), [handles]);
  const liveScript = useMemo(() => mapEventsToScript(events), [events]);
  const isLive = live && liveScript.length > 0;

  return isLive
    ? { agents: liveAgents, script: liveScript, hand: LIVE_HAND, live: true }
    : { agents: GAGENTS, script: GSCRIPT, hand: HAND, live: false };
}
