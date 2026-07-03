"use client";

import { useMemo } from "react";
import { AgentAvatar } from "./AgentAvatar";
import { profileFor, STATUS_LABEL } from "@/lib/agentIdentity";
import { cx } from "@/components/ui";

export interface RoundTableSeat {
  id: number;
  name: string;
  owner?: string;
  alive: boolean;
  speaking?: boolean;
  voting?: boolean;
  thinking?: boolean;
  suspected?: boolean;
  voteTarget?: number;
  incomingVotes?: number;
  showRole?: boolean;
  roleLabel?: string;
}

/** Circular agent table — PDF: avatars, owner, status, voice glow, vote arrows. */
export function MafiaRoundTable({
  seats,
  votes,
  focusId,
  votingMode,
  speech,
  onSeatClick,
  className,
}: {
  seats: RoundTableSeat[];
  votes?: { from: number; target: number }[];
  focusId?: number | null;
  votingMode?: boolean;
  speech?: { id: number; text: string } | null;
  onSeatClick?: (seat: RoundTableSeat) => void;
  className?: string;
}) {
  const positions = useMemo(() => {
    const n = seats.length;
    const radius = 42;
    return seats.map((seat, i) => {
      const angle = (i / n) * 2 * Math.PI - Math.PI / 2;
      return {
        seat,
        x: 50 + radius * Math.cos(angle),
        y: 50 + radius * Math.sin(angle),
      };
    });
  }, [seats]);

  const seatById = useMemo(() => {
    const m = new Map<number, { x: number; y: number }>();
    positions.forEach(({ seat, x, y }) => m.set(seat.id, { x, y }));
    return m;
  }, [positions]);

  return (
    <div
      className={cx(
        "mafia-round-table relative mx-auto aspect-square w-full max-w-[min(100%,72vh,1040px)]",
        votingMode && "mafia-table-voting",
        className,
      )}
    >
      {/* Table — themeable rim, felt, inner guide ring, accent medallion */}
      <div className="mf-rim absolute inset-[13%] rounded-full" />
      <div className="mf-felt absolute inset-[16.5%] rounded-full" />
      <div className="mf-guide absolute inset-[25%] rounded-full" />
      <div className="mf-medal absolute inset-[37%] rounded-full" />

      {/* Vote lasers — danger color, glow underlay, animated draw */}
      <svg className="pointer-events-none absolute inset-0 h-full w-full" viewBox="0 0 100 100" preserveAspectRatio="none">
        <defs>
          <marker id="vote-arrowhead" markerWidth="5" markerHeight="5" refX="3.4" refY="2.5" orient="auto">
            <polygon points="0 0, 5 2.5, 0 5" className="mf-arrowhead" />
          </marker>
        </defs>
        {votes?.map((v, i) => {
          const from = seatById.get(v.from);
          const to = seatById.get(v.target);
          if (!from || !to) return null;
          return (
            <g key={`${v.from}-${v.target}-${i}`}>
              <line x1={from.x} y1={from.y} x2={to.x} y2={to.y} className="mf-vote-glow" strokeWidth="1.8" strokeLinecap="round" />
              <line
                x1={from.x}
                y1={from.y}
                x2={to.x}
                y2={to.y}
                className="mf-vote-laser"
                strokeWidth="0.7"
                strokeLinecap="round"
                markerEnd="url(#vote-arrowhead)"
              />
              <circle cx={to.x} cy={to.y} r="1.7" className="mf-vote-target" fill="rgb(var(--c-status-error) / 0.9)" />
            </g>
          );
        })}
      </svg>

      {/* Seats */}
      {positions.map(({ seat, x, y }) => {
        const profile = profileFor(seat.id, seat.name);
        const owner = seat.owner ?? profile.owner;
        const dead = !seat.alive;
        const focused = focusId === seat.id;
        const status =
          seat.thinking ? "thinking"
          : seat.voting ? "voting"
          : seat.suspected ? "suspected"
          : seat.speaking ? "speaking"
          : dead ? "dead"
          : "alive";

        return (
          <div
            key={seat.id}
            onClick={() => onSeatClick?.(seat)}
            className={cx(
              "absolute flex -translate-x-1/2 -translate-y-1/2 flex-col items-center gap-0.5 transition-transform duration-300",
              onSeatClick && "cursor-pointer hover:z-20 hover:scale-[1.07]",
              focused && "z-10 scale-110",
              dead && "opacity-50",
            )}
            style={{ left: `${x}%`, top: `${y}%` }}
          >
            {dead ? (
              <div className="mafia-empty-seat flex h-[72px] w-[72px] items-center justify-center rounded-full border-2 border-dashed border-border-soft bg-bg-deep/60 animate-death-fade">
                <span className="font-mono text-[9px] uppercase text-ink-faint">Out</span>
              </div>
            ) : (
              <div
                className={cx(
                  "relative",
                  seat.speaking && "mafia-seat-speaking",
                  seat.incomingVotes != null && seat.incomingVotes > 0 && "mf-target-react",
                )}
              >
                {seat.speaking && <span className="mafia-speaking-ring" aria-hidden />}
                <AgentAvatar
                  profile={profile}
                  size="lg"
                  speaking={seat.speaking}
                  voting={seat.voting}
                  status={status}
                />
                {seat.thinking && (
                  <span className="absolute -bottom-1 left-1/2 flex -translate-x-1/2 gap-0.5 rounded-full bg-bg-deep px-1.5 py-0.5">
                    <span className="mafia-thinking-dot h-1 w-1 rounded-full bg-tertiary" />
                    <span className="mafia-thinking-dot h-1 w-1 rounded-full bg-tertiary [animation-delay:120ms]" />
                    <span className="mafia-thinking-dot h-1 w-1 rounded-full bg-tertiary [animation-delay:240ms]" />
                  </span>
                )}
              </div>
            )}

            <span
              className={cx(
                "max-w-[80px] truncate text-center font-display text-[11px] font-semibold leading-tight",
                dead ? "text-ink-faint line-through" : "text-ink-primary",
                seat.speaking && "text-primary",
                seat.suspected && !seat.speaking && "text-secondary",
              )}
            >
              {seat.name.split("_")[0]}
            </span>
            {!dead && (
              <span className="max-w-[84px] truncate font-mono text-[9px] text-ink-dim">{owner}</span>
            )}
            {!dead && (
              <span
                className={cx(
                  "font-mono text-[8px] uppercase tracking-wider",
                  seat.speaking ? "text-tertiary" : seat.suspected ? "text-secondary" : "text-primary/70",
                )}
              >
                {STATUS_LABEL[status === "dead" ? "dead" : status]}
              </span>
            )}
            {seat.incomingVotes != null && seat.incomingVotes > 0 && seat.alive && (
              <span className="rounded-full bg-status-error/15 px-1.5 font-mono text-[9px] font-semibold text-status-error">
                {seat.incomingVotes} votes
              </span>
            )}
            {seat.showRole && seat.roleLabel && (
              <span className="font-mono text-[8px] uppercase tracking-wider text-ink-faint">{seat.roleLabel}</span>
            )}
          </div>
        );
      })}

      {/* Active speaker's line — discussion happens AT the table */}
      {speech &&
        (() => {
          const c = seatById.get(speech.id);
          const sp = seats.find((s) => s.id === speech.id);
          if (!c || !sp || !sp.alive) return null;
          const bx = c.x + (50 - c.x) * 0.34;
          const by = c.y + (50 - c.y) * 0.34;
          const text = speech.text.length > 150 ? speech.text.slice(0, 148).trimEnd() + "…" : speech.text;
          return (
            <div
              key={`${speech.id}-${speech.text.slice(0, 12)}`}
              className="mf-speech pointer-events-none absolute z-[8] w-[min(40%,250px)]"
              style={{ left: `${bx}%`, top: `${by}%` }}
            >
              <div className="rounded-xl border border-primary-container/45 bg-surface-bright/92 px-3 py-2 text-[11px] leading-snug text-ink-primary shadow-glow-teal backdrop-blur-sm">
                <span className="font-display font-semibold text-primary">{sp.name.split("_")[0]}:</span>{" "}
                <span className="text-ink-dim">{text}</span>
              </div>
            </div>
          );
        })()}

      <div className="pointer-events-none absolute left-1/2 top-1/2 z-[1] flex -translate-x-1/2 -translate-y-1/2 flex-col items-center text-center">
        <span className="label-caps text-ink-faint">Round Table</span>
        <p className="font-display text-5xl font-bold leading-none text-ink-primary">
          {seats.filter((s) => s.alive).length}
        </p>
        <span className="mt-1.5 font-mono text-[11px] font-semibold uppercase tracking-caps text-emerald-500">Agents Alive</span>
        {votingMode && (
          <span className="mt-2 inline-flex items-center gap-1.5 rounded-full bg-status-error/12 px-2.5 py-1 font-mono text-[10px] font-semibold uppercase tracking-caps text-status-error">
            <span className="live-dot h-1.5 w-1.5 rounded-full bg-status-error" /> Voting
          </span>
        )}
      </div>
    </div>
  );
}
