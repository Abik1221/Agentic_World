"use client";

// ---------------------------------------------------------------------------
// useMonopolyLiveScript — feeds the scripted MonopolyViewer with LIVE data.
//
// The viewer (components/monopoly/MonopolyViewer.tsx) is built around two demo
// imports: MAGENTS (the cast) and MSCRIPT (an ordered list of MStep beats).
// This hook returns the SAME shapes ({ agents, script }) so the viewer can
// render a real streaming match with zero changes to its animation/JSX — it
// just reads its data from here instead of the module-level demo constants.
//
// Live path: discover a match (fetchMonopolyLive or a preferredMatchId), open
// the SSE stream (monopolyWatchUrl + EventSource), parse the {seq,type,payload}
// frames, then fold those numeric-seat engine events into the viewer's
// agent-id-based MStep[]. A synthesized N-player cast backs the seat ids.
//
// Demo is the HARD DEFAULT. When there is no live match, any error, or the
// backend is offline, the hook returns the demo MAGENTS and MSCRIPT unchanged
// (live:false) so the viewer renders exactly as it does today.
// ---------------------------------------------------------------------------

import { useEffect, useMemo, useRef, useState } from "react";
import { fetchMonopolyLive, monopolyWatchUrl, type MonopolyLogEvent } from "./api";
import { BOARD, MAGENTS, MSCRIPT, type MAgent, type MStep } from "./monopoly-demo";

const CONNECT_GRACE_MS = 3500;

// The Monopoly engine emits NAMED SSE events (`event: <type>` + a
// {seq,type,payload} data line), so EventSource never fires onmessage for them —
// we register the same handler for each type name. This is the full engine
// EventType set; only a subset is mapped to a viewer beat below.
const MONO_EVENTS = [
  "match_created", "turn_started", "dice_rolled", "moved", "cash_changed",
  "rent_paid", "property_purchased", "card_drawn", "went_to_jail", "left_jail",
  "house_built", "house_sold", "mortgaged", "unmortgaged", "auction_started",
  "bid_placed", "auction_passed", "auction_won", "auction_unsold", "bankrupt",
  "trade_proposed", "trade_executed", "trade_rejected", "turn_ended", "match_finished",
];

// A neutral palette for the synthetic live cast (one colour per seat).
const SEAT_PALETTE = [
  "#6366f1", "#22c55e", "#f59e0b", "#ec4899",
  "#ef4444", "#14b8a6", "#8b5cf6", "#eab308",
];

function seatId(n: number): string {
  return `seat-${n}`;
}

// Synthesize a stable N-player cast from the match. Real handles fill `name`
// where the table exposed them (bots fall back to "Seat i"). Every field of the
// viewer's MAgent type is filled; startCash is the engine's standard 1500.
function buildLiveAgents(players: number, teams: string[]): MAgent[] {
  const n = Math.max(2, players || teams.length || 4);
  return Array.from({ length: n }, (_, i) => ({
    id: seatId(i),
    name: teams[i] || `Seat ${i}`,
    dev: "live",
    color: SEAT_PALETTE[i % SEAT_PALETTE.length],
    model: "live",
    sdk: "—",
    winRate: 0,
    startCash: 1500,
  }));
}

const propName = (i: number): string => BOARD[i]?.name ?? `#${i}`;

// Human label for one side of a trade: property names joined with a cash rider.
function tradeLabel(props: number[], cash: number): string {
  const parts = props.map(propName);
  if (cash > 0) parts.push(`$${cash}`);
  return parts.join(" + ") || (cash > 0 ? `$${cash}` : "—");
}

const num = (v: unknown, d = 0): number => (typeof v === "number" ? v : d);

// Fold the numeric-seat live event stream into the viewer's MStep[] beats.
// turn_started carries the running turn/player; a dice_rolled is paired with the
// following moved for the same seat into a single "roll" beat. Cash-only motions
// (tax/cards/salary) and jail/auction events have no viewer beat and are skipped.
function mapEventsToScript(events: MonopolyLogEvent[]): MStep[] {
  const steps: MStep[] = [];
  let turn = 1;
  let pendingDice: { seat: number; dice: [number, number] } | null = null;

  for (const e of events) {
    const p = (e.payload ?? {}) as Record<string, unknown>;
    switch (e.type) {
      case "turn_started": {
        turn = num(p.turn_count, turn);
        break;
      }
      case "dice_rolled": {
        pendingDice = { seat: num(p.seat), dice: [num(p.die1, 1), num(p.die2, 1)] };
        break;
      }
      case "moved": {
        const seat = num(p.seat);
        const to = num(p.to);
        if (pendingDice && pendingDice.seat === seat) {
          const sum = pendingDice.dice[0] + pendingDice.dice[1];
          steps.push({
            turn,
            player: seatId(seat),
            kind: "roll",
            dice: pendingDice.dice,
            to,
            event: `Seat ${seat} rolls ${sum} → ${propName(to)}`,
          });
          pendingDice = null;
        }
        break;
      }
      case "property_purchased": {
        const seat = num(p.seat);
        const prop = num(p.property);
        steps.push({
          turn,
          player: seatId(seat),
          kind: "buy",
          buy: prop,
          event: `Seat ${seat} buys ${propName(prop)} ($${num(p.price)})`,
        });
        break;
      }
      case "rent_paid": {
        const from = num(p.from);
        const to = num(p.to);
        const amount = num(p.amount);
        steps.push({
          turn,
          player: seatId(from),
          kind: "rent",
          rent: { from: seatId(from), to: seatId(to), amount },
          event: `Seat ${from} pays $${amount} rent to Seat ${to}`,
        });
        break;
      }
      case "house_built": {
        const seat = num(p.seat);
        const prop = num(p.property);
        steps.push({
          turn,
          player: seatId(seat),
          kind: "build",
          build: { spaces: [prop] },
          event: `Seat ${seat} builds on ${propName(prop)}`,
        });
        break;
      }
      case "trade_proposed":
      case "trade_executed":
      case "trade_rejected": {
        const proposer = num(p.proposer);
        const target = num(p.target);
        const giveProps = Array.isArray(p.give_props) ? (p.give_props as number[]) : [];
        const wantProps = Array.isArray(p.want_props) ? (p.want_props as number[]) : [];
        const giveCash = num(p.give_cash);
        const wantCash = num(p.want_cash);
        const status = e.type === "trade_executed" ? "accept" : e.type === "trade_rejected" ? "reject" : "propose";
        steps.push({
          turn,
          player: seatId(proposer),
          kind: "trade",
          trade: {
            from: seatId(proposer),
            to: seatId(target),
            give: tradeLabel(giveProps, giveCash),
            get: tradeLabel(wantProps, wantCash),
            status,
            giveIdx: giveProps[0],
            getIdx: wantProps[0],
            cash: giveCash - wantCash,
          },
          event:
            status === "accept"
              ? `Trade accepted: Seat ${proposer} ↔ Seat ${target}`
              : status === "reject"
                ? `Seat ${target} rejects the trade`
                : `Seat ${proposer} proposes a trade to Seat ${target}`,
        });
        break;
      }
      // cash_changed / card_drawn / jail / auction / bankrupt / match_finished
      // have no faithful MStep kind — skipped (no invented UI).
    }
  }

  return steps;
}

export interface MonopolyLiveScript {
  agents: MAgent[];
  script: MStep[];
  live: boolean;
}

// useMonopolyLiveScript returns the cast + script the MonopolyViewer should
// render. It streams a live match when one is available and maps it into the
// viewer's own shapes; otherwise it returns the scripted demo unchanged.
export function useMonopolyLiveScript(preferredMatchId?: string): MonopolyLiveScript {
  const [events, setEvents] = useState<MonopolyLogEvent[]>([]);
  const [meta, setMeta] = useState<{ players: number; teams: string[] } | null>(null);
  const [live, setLive] = useState(false);
  const gotEvent = useRef(false);

  useEffect(() => {
    // Spectating is a public SSE stream, so we always attempt to find and stream
    // a live match; when none is reachable we fall back to the demo below.
    let cancelled = false;
    let es: EventSource | null = null;
    let graceTimer: ReturnType<typeof setTimeout> | null = null;
    const ctrl = new AbortController();

    gotEvent.current = false;
    setEvents([]);
    setMeta(null);
    setLive(false);

    const connect = (id: string, m: { players: number; teams: string[] }) => {
      try {
        es = new EventSource(monopolyWatchUrl(id));
      } catch {
        return; // demo default stays in place
      }

      const onMsg = (msg: MessageEvent) => {
        let parsed: MonopolyLogEvent;
        try {
          parsed = JSON.parse(msg.data);
        } catch {
          return; // keep-alive / non-JSON frame
        }
        if (typeof parsed?.type !== "string") return;
        gotEvent.current = true;
        setMeta(m);
        setLive(true);
        setEvents((prev) => [...prev, parsed]);
      };

      MONO_EVENTS.forEach((t) => es!.addEventListener(t, onMsg as EventListener));
      es.onmessage = onMsg; // in case any frame is emitted unnamed
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
      const matches = await fetchMonopolyLive(ctrl.signal);
      let id = preferredMatchId;
      let m = { players: 4, teams: [] as string[] };
      if (!id) {
        // Prefer an in-progress table over a finished one; else nothing.
        const pick = matches.find((x) => x.winner == null) ?? matches[0];
        id = pick?.matchId;
        if (pick) m = { players: pick.players ?? pick.teams.length ?? 4, teams: pick.teams ?? [] };
      } else {
        const found = matches.find((x) => x.matchId === id);
        if (found) m = { players: found.players ?? found.teams.length ?? 4, teams: found.teams ?? [] };
      }
      if (cancelled || !id) return;
      connect(id, m);
    })();

    return () => {
      cancelled = true;
      ctrl.abort();
      if (graceTimer) clearTimeout(graceTimer);
      es?.close();
    };
  }, [preferredMatchId]);

  const liveAgents = useMemo(() => (meta ? buildLiveAgents(meta.players, meta.teams) : MAGENTS), [meta]);
  const liveScript = useMemo(() => mapEventsToScript(events), [events]);
  const isLive = live && liveScript.length > 0;

  return isLive
    ? { agents: liveAgents, script: liveScript, live: true }
    : { agents: MAGENTS, script: MSCRIPT, live: false };
}
