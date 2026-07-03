"use client";

import { ReactNode } from "react";
import { Pill, SectionLabel, cx, type Tone } from "@/components/ui";

/** Esports broadcast shell — top bar, live area, side rail, bottom commentary. */
export function SpectatorBroadcast({
  gameType,
  tournament = "Open Arena",
  prizePool,
  timer,
  phase,
  phaseTone = "teal",
  live,
  children,
  sidebar,
  commentary,
  className,
}: {
  gameType: string;
  tournament?: string;
  prizePool: string;
  timer?: string;
  phase?: string;
  phaseTone?: Tone;
  live?: boolean;
  children: ReactNode;
  sidebar?: ReactNode;
  commentary?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cx("broadcast-shell overflow-hidden rounded-xl border border-border-strong", className)}>
      {/* Top broadcast bar */}
      <header className="broadcast-top flex flex-wrap items-center justify-between gap-4 border-b border-border-soft px-5 py-4">
        <div className="flex flex-wrap items-center gap-4">
          <div>
            <SectionLabel className="text-ink-faint">Now watching</SectionLabel>
            <h1 className="font-display text-2xl font-bold tracking-tight text-ink-primary md:text-3xl">
              {gameType}
            </h1>
            <p className="mt-0.5 font-mono text-[12px] text-ink-dim">{tournament}</p>
          </div>
          {live && (
            <Pill tone="teal" dot className="self-start">
              LIVE
            </Pill>
          )}
          {phase && (
            <Pill tone={phaseTone} dot className="self-start">
              {phase}
            </Pill>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-6">
          <BroadcastStat label="Prize pool" value={prizePool} highlight />
          {timer && <BroadcastStat label="Match time" value={timer} />}
        </div>
      </header>

      {/* Main stage + side panel */}
      <div className="grid gap-0 lg:grid-cols-[1fr_minmax(260px,300px)]">
        <main className="broadcast-main min-h-[480px] p-4 md:p-6">{children}</main>
        {sidebar && (
          <aside className="broadcast-side border-t border-border-soft bg-bg-deep/40 p-4 lg:border-l lg:border-t-0">
            {sidebar}
          </aside>
        )}
      </div>

      {/* Bottom commentary strip */}
      {commentary && (
        <footer className="broadcast-commentary border-t border-border-soft bg-surface-slate/50 px-4 py-3 md:px-6">
          {commentary}
        </footer>
      )}
    </div>
  );
}

function BroadcastStat({
  label,
  value,
  highlight,
}: {
  label: string;
  value: string;
  highlight?: boolean;
}) {
  return (
    <div className="text-right">
      <div className="label-caps text-ink-faint">{label}</div>
      <div
        className={cx(
          "font-mono text-xl font-semibold tabular-nums md:text-2xl",
          highlight ? "text-primary" : "text-ink-primary",
        )}
      >
        {value}
      </div>
    </div>
  );
}

export function CommentaryStrip({
  title = "Live commentary",
  children,
}: {
  title?: string;
  children: ReactNode;
}) {
  return (
    <div>
      <SectionLabel className="mb-2 text-primary">{title}</SectionLabel>
      <div className="font-mono text-[13px] leading-6 text-ink-dim">{children}</div>
    </div>
  );
}
