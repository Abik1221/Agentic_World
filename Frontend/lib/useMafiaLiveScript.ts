"use client";

// ---------------------------------------------------------------------------
// useMafiaLiveScript — feeds the scripted MafiaViewer with LIVE data.
//
// The viewer (components/mafia/MafiaViewer.tsx) is built around two demo
// imports: AGENTS (the cast) and SCRIPT (an ordered list of Step beats). This
// hook returns the SAME shapes ({ agents, script }) so the viewer can render a
// real streaming match with zero changes to its animation/JSX — it just reads
// its data from here instead of the module-level demo constants.
//
// Live path: discover a match (fetchMafiaLive or a preferredMatchId), open the
// SSE stream (mafiaWatchUrl + EventSource), parse frames into the MafiaEvent
// union (mafiaEventFromWire), then fold those numeric-seat events into the
// viewer's agent-id-based Step[]. A synthetic 12-seat cast backs the seat ids.
//
// Demo is the HARD DEFAULT. When there is no live match, any error, the backend
// is offline, the hook returns the demo AGENTS and
// SCRIPT unchanged (live:false) so the viewer renders exactly as it does today.
// ---------------------------------------------------------------------------

import { useEffect, useMemo, useRef, useState } from "react";
import { fetchMafiaLive, mafiaWatchUrl } from "./api";
import {
  mafiaEventFromWire,
  type MafiaEvent,
  type MafiaPhase,
  type MafiaWireEvent,
  type MsgTone,
} from "./mafia";
import {
  AGENTS,
  SCRIPT,
  type Agent,
  type Intent,
  type Phase,
  type Step,
} from "./mafia-demo";

const CONNECT_GRACE_MS = 3500;
const NAMED_EVENTS = ["phase", "moderator", "night", "message", "vote", "eliminate", "victory"];

// A neutral 12-colour palette for the synthetic live cast (one per seat).
const SEAT_PALETTE = [
  "#6366f1", "#8b5cf6", "#f59e0b", "#22c55e",
  "#ef4444", "#ec4899", "#14b8a6", "#eab308",
  "#38bdf8", "#a3e635", "#f97316", "#c084fc",
];

// The live engine seats 12 agents. We synthesize a stable cast once (same
// reference across renders) so the viewer's cast-derived memos stay stable.
// Every field of the viewer's Agent type is filled with a sensible default;
// real handles/models are redacted while a match is live, so short placeholders
// are correct here — the roster panel, not the roles, is what's on show.
function buildLiveAgents(): Agent[] {
  return Array.from({ length: 12 }, (_, i) => ({
    id: seatId(i + 1),
    name: `Seat ${i + 1}`,
    dev: "live",
    color: SEAT_PALETTE[i % SEAT_PALETTE.length],
    model: "live",
    sdk: "—",
    manifest: "—",
    winRate: 0,
    games: 0,
    responseMs: 0,
    role: "citizen", // ground-truth roles are redacted during a live match
  }));
}

const LIVE_AGENTS: Agent[] = buildLiveAgents();

function seatId(n: number): string {
  return `seat-${n}`;
}

// Live MafiaPhase → the viewer's 4-state Phase. Morning/result have no distinct
// viewer phase, so they fold into "execution" (the reveal/aftermath beat).
function mapPhase(p: MafiaPhase): Phase {
  switch (p) {
    case "night":
      return "night";
    case "discussion":
      return "discussion";
    case "voting":
      return "voting";
    case "morning":
    case "result":
      return "execution";
    default:
      return "discussion";
  }
}

// MsgTone → the closest viewer Intent (drives the chat badge + suspicion bumps).
function toneToIntent(tone: MsgTone): Intent {
  switch (tone) {
    case "accuse":
      return "accusing";
    case "defend":
      return "defending";
    case "claim":
      return "bluffing";
    case "info":
      return "analyzing";
    case "alliance":
      return "negotiating";
    default:
      return "thinking";
  }
}

// Fold the numeric-seat live event stream into the viewer's Step[] beats,
// tracking the current phase/day so messages and votes carry the right context.
// Night events are skipped (live matches redact night details).
function mapEventsToScript(events: MafiaEvent[]): Step[] {
  const steps: Step[] = [];
  let day = 1;
  let phase: Phase = "discussion";

  for (const e of events) {
    switch (e.kind) {
      case "phase":
        day = e.day;
        phase = mapPhase(e.phase);
        break;
      case "message":
        steps.push({ phase, day, speaker: seatId(e.from), text: e.text, intent: toneToIntent(e.tone) });
        break;
      case "vote":
        steps.push({ phase: "voting", day, vote: { from: seatId(e.from), to: seatId(e.target) } });
        break;
      case "eliminate":
        steps.push({ phase, day, eliminate: seatId(e.target), event: `Seat ${e.target} eliminated (${e.cause})` });
        break;
      case "moderator":
        steps.push({ phase, day, event: e.text });
        break;
      case "victory":
        steps.push({ phase, day, event: e.text });
        break;
      case "night":
        break; // redacted while live — skip
    }
  }

  return steps;
}

export interface MafiaLiveScript {
  agents: Agent[];
  script: Step[];
  live: boolean;
}

// useMafiaLiveScript returns the cast + script the MafiaViewer should render.
// It streams a live match when one is available and maps it into the viewer's
// own shapes; otherwise it returns the scripted demo unchanged.
export function useMafiaLiveScript(preferredMatchId?: string): MafiaLiveScript {
  const [events, setEvents] = useState<MafiaEvent[]>([]);
  const [live, setLive] = useState(false);
  const gotEvent = useRef(false);

  useEffect(() => {
    // Spectating is a public SSE stream with no auth to surface, so we always
    // attempt to find and stream a live match regardless of STRICT; when none is
    // reachable we simply fall back to the demo script below. (STRICT still gates
    // the mock-vs-real behaviour of the authenticated lib/api.ts calls elsewhere.)

    let cancelled = false;
    let es: EventSource | null = null;
    let graceTimer: ReturnType<typeof setTimeout> | null = null;
    const ctrl = new AbortController();

    gotEvent.current = false;
    setEvents([]);
    setLive(false);

    const connect = (id: string) => {
      try {
        es = new EventSource(mafiaWatchUrl(id));
      } catch {
        return; // demo default stays in place
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
        setLive(true);
        setEvents((prev) => [...prev, ev]);
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
      let id = preferredMatchId;
      if (!id) {
        const matches = await fetchMafiaLive(ctrl.signal);
        // Prefer an in-progress table over a finished one; else nothing.
        const pick = matches.find((m) => !m.winner) ?? matches[0];
        id = pick?.matchId;
      }
      if (cancelled || !id) return;
      connect(id);
    })();

    return () => {
      cancelled = true;
      ctrl.abort();
      if (graceTimer) clearTimeout(graceTimer);
      es?.close();
    };
  }, [preferredMatchId]);

  const liveScript = useMemo(() => mapEventsToScript(events), [events]);
  const isLive = live && liveScript.length > 0;

  return isLive
    ? { agents: LIVE_AGENTS, script: liveScript, live: true }
    : { agents: AGENTS, script: SCRIPT, live: false };
}
