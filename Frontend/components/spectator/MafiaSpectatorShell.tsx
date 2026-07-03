"use client";

import { ReactNode } from "react";
import { Pill, SectionLabel, cx, type Tone } from "@/components/ui";

/** PDF layout: top bar → table + chat → bottom intel. */
export function MafiaSpectatorShell({
  tournament,
  prizePool,
  round,
  dayNight,
  timer,
  live,
  phase,
  phaseTone = "teal",
  voting,
  stageClass,
  headerExtra,
  table,
  chat,
  bottom,
  className,
}: {
  tournament: string;
  prizePool: string;
  round: string;
  dayNight: string;
  timer: string;
  live?: boolean;
  phase?: string;
  phaseTone?: Tone;
  voting?: boolean;
  stageClass?: string;
  headerExtra?: ReactNode;
  table: ReactNode;
  chat: ReactNode;
  bottom: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cx(
        "mafia-spectator overflow-hidden rounded-2xl border border-border-strong bg-surface-slate/80 shadow-[0_24px_70px_-20px_rgba(120,96,60,0.28)]",
        stageClass,
        voting && "is-voting",
        className,
      )}
    >
      {/* Top — Tournament | Prize Pool | Round | Day/Night | Timer */}
      <header className="mafia-spectator-top grid gap-3 border-b border-border-soft px-4 py-3 md:grid-cols-5 md:px-5 md:py-4">
        <TopCell label="Tournament" value={tournament} />
        <TopCell label="Prize pool" value={prizePool} highlight />
        <TopCell label="Round" value={round} />
        <TopCell
          label="Day / night"
          value={dayNight}
          accent={dayNight === "Night" ? "blue" : "amber"}
        />
        <div className="flex items-end justify-between gap-2 md:flex-col md:items-end">
          <TopCell label="Timer" value={timer} className="text-right" />
          <div className="flex flex-wrap items-center gap-2">
            {live && (
              <Pill tone="teal" dot>
                LIVE
              </Pill>
            )}
            {phase && (
              <Pill tone={phaseTone} dot>
                {phase}
              </Pill>
            )}
            {headerExtra}
          </div>
        </div>
      </header>

      {/* Main — dominant table (left) · narrow scrollable chat rail (right) */}
      <div className="mafia-spectator-main grid min-h-0 grid-rows-[minmax(0,1fr)_360px] lg:grid-cols-[minmax(0,1fr)_minmax(340px,420px)] lg:grid-rows-none lg:divide-x lg:divide-border-soft">
        <section className="relative flex min-h-0 items-center justify-center p-4 md:p-6 lg:p-8">
          <div className="mafia-orb pointer-events-none" aria-hidden="true" />
          <div className="mafia-stars pointer-events-none" aria-hidden="true" />
          <div className="relative z-[1] flex h-full w-full items-center justify-center">{table}</div>
        </section>
        <section className="flex min-h-0 flex-col overflow-hidden border-t border-border-soft bg-surface-container/55 lg:border-t-0">
          {chat}
        </section>
      </div>

      {/* Bottom — timeline, agents, votes, stats (secondary) */}
      <footer className="mafia-spectator-bottom border-t border-border-soft bg-surface-container/45 px-4 py-4 md:px-5">
        {bottom}
      </footer>
    </div>
  );
}

function TopCell({
  label,
  value,
  highlight,
  accent,
  className,
}: {
  label: string;
  value: string;
  highlight?: boolean;
  accent?: Tone;
  className?: string;
}) {
  return (
    <div className={className}>
      <SectionLabel className="text-ink-faint">{label}</SectionLabel>
      <p
        className={cx(
          "mt-1 font-display text-lg font-semibold tabular-nums text-ink-primary md:text-xl",
          highlight && "text-primary",
          accent === "blue" && "text-tertiary",
          accent === "amber" && "text-secondary",
          accent === "teal" && "text-primary",
        )}
      >
        {value}
      </p>
    </div>
  );
}
