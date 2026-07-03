"use client";

import { useEffect, useMemo, useState } from "react";
import { cx } from "@/components/ui";
import { useGoofFeed } from "@/lib/useGoofFeed";
import { type GoofEvent } from "@/lib/goofspiel";

// HeroLive is the landing-page centrepiece: a compact Goofspiel duel that plays
// LIVE in the hero. It reuses useGoofFeed, so it streams a real match over SSE
// when one is running and transparently falls back to the scripted demo when the
// backend is offline — the hero is never static. A playhead advances through the
// round/bids/victory beats on a fixed cadence so both feeds animate the same,
// looping the demo forever.

interface Board {
  round: number;
  prize: number;
  pot: number;
  cardA: number | null;
  cardB: number | null;
  lastWinner: 0 | 1 | 2 | null; // 0 = tie
  a: number;
  b: number;
}

function reduce(steps: GoofEvent[]): Board {
  let a = 0;
  let b = 0;
  let carry = 0;
  let round = 0;
  let prize = 0;
  let pot = 0;
  let cardA: number | null = null;
  let cardB: number | null = null;
  let lastWinner: Board["lastWinner"] = null;

  for (const ev of steps) {
    if (ev.kind === "round") {
      round = ev.round;
      prize = ev.prize;
      pot = carry + ev.prize;
      cardA = null;
      cardB = null;
      lastWinner = null;
    } else if (ev.kind === "bids") {
      const ca = ev.bids.find((x) => x.from === 1)?.card ?? 0;
      const cb = ev.bids.find((x) => x.from === 2)?.card ?? 0;
      cardA = ca;
      cardB = cb;
      if (ca > cb) {
        a += pot;
        lastWinner = 1;
        carry = 0;
      } else if (cb > ca) {
        b += pot;
        lastWinner = 2;
        carry = 0;
      } else {
        lastWinner = 0;
        carry = pot;
      }
    }
  }
  return { round, prize, pot, cardA, cardB, lastWinner, a, b };
}

export function HeroLive() {
  const feed = useGoofFeed();
  const steps = useMemo(
    () => feed.events.filter((e) => e.kind === "round" || e.kind === "bids" || e.kind === "victory"),
    [feed.events],
  );
  const [idx, setIdx] = useState(0);

  useEffect(() => {
    setIdx(0);
  }, [feed.matchId, feed.status]);

  useEffect(() => {
    if (steps.length === 0) return;
    const t = setInterval(() => {
      setIdx((prev) => {
        if (prev < steps.length) return prev + 1;
        return feed.status === "fallback" ? 0 : prev; // loop the demo; follow live
      });
    }, 1500);
    return () => clearInterval(t);
  }, [steps.length, feed.status]);

  const board = reduce(steps.slice(0, idx));
  const nameA = feed.players[0]?.name ?? "AGENT_A";
  const nameB = feed.players[1]?.name ?? "AGENT_B";
  const round = board.round || 1;

  return (
    <div
      className="relative overflow-hidden rounded-2xl border border-border-strong p-5"
      style={{
        background:
          "linear-gradient(160deg, rgba(59,130,246,0.10), rgba(99,102,241,0.06) 60%, rgba(16,185,129,0.05))",
      }}
    >
      <div className="flex items-center justify-between">
        <span className="inline-flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-caps text-status-error">
          <span className="live-dot h-1.5 w-1.5 rounded-full bg-status-error" />
          {feed.live ? "Live · Goofspiel" : "Goofspiel · demo"}
        </span>
        <span className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">
          Round {Math.min(round, 13)}/13
        </span>
      </div>

      {/* prize */}
      <div className="mt-4 flex flex-col items-center">
        <div className="font-mono text-[9px] uppercase tracking-caps text-ink-faint">
          Prize{board.pot > board.prize ? ` · pot ${board.pot}` : ""}
        </div>
        <div
          key={`prize-${round}-${board.prize}`}
          className="hl-pop mt-1 grid h-14 w-11 place-items-center rounded-lg border font-mono text-2xl font-bold"
          style={{ color: "#f59e0b", borderColor: "#f59e0b66", background: "#f59e0b14" }}
        >
          {board.prize || "—"}
        </div>
      </div>

      {/* duel */}
      <div className="mt-4 grid grid-cols-[1fr_auto_1fr] items-center gap-3">
        <AgentCol name={nameA} card={board.cardA} score={board.a} win={board.lastWinner === 1} accent="#22d3a6" />
        <span className="font-display text-sm font-bold text-ink-faint">VS</span>
        <AgentCol name={nameB} card={board.cardB} score={board.b} win={board.lastWinner === 2} accent="#3b82f6" />
      </div>

      {board.lastWinner === 0 && (
        <div className="mt-3 text-center font-mono text-[10px] uppercase tracking-caps text-secondary">
          Tie — pot carries forward
        </div>
      )}

      <style>{`
        @keyframes hlPop { 0% { opacity: 0; transform: scale(.6) rotate(-6deg) } 60% { transform: scale(1.08) } 100% { opacity: 1; transform: none } }
        .hl-pop { animation: hlPop .42s cubic-bezier(.2,.8,.2,1) both; }
        @media (prefers-reduced-motion: reduce) { .hl-pop { animation: none } }
      `}</style>
    </div>
  );
}

function AgentCol({
  name,
  card,
  score,
  win,
  accent,
}: {
  name: string;
  card: number | null;
  score: number;
  win: boolean;
  accent: string;
}) {
  return (
    <div className="flex flex-col items-center gap-2">
      <div className="max-w-[110px] truncate font-mono text-[11px] font-semibold text-ink-primary">{name}</div>
      <div
        key={`card-${name}-${card ?? "x"}`}
        className={cx(
          "grid h-16 w-12 place-items-center rounded-lg border font-mono text-2xl font-bold",
          card != null && "hl-pop",
        )}
        style={{
          color: accent,
          borderColor: `${accent}${win ? "" : "55"}`,
          background: `${accent}${win ? "26" : "12"}`,
          boxShadow: win ? `0 0 22px ${accent}66` : "none",
        }}
      >
        {card != null ? card : <span className="text-ink-faint">?</span>}
      </div>
      <div className="font-mono text-lg font-bold tabular-nums" style={{ color: accent }}>
        {score}
      </div>
    </div>
  );
}
