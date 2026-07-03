"use client";

import { useEffect, useState } from "react";
import { cx } from "@/components/ui";

/** Slowly drifting live number, so the HUD always feels alive (esports telemetry). */
function useDrift(base: number, jitter: number, ms = 2000) {
  const [v, setV] = useState(base);
  useEffect(() => {
    const id = setInterval(
      () => setV((prev) => Math.max(0, Math.round(base + (prev - base) * 0.6 + (Math.random() - 0.45) * jitter))),
      ms,
    );
    return () => clearInterval(id);
  }, [base, jitter, ms]);
  return v;
}

const fmtNum = (n: number) => n.toLocaleString("en-US");

const TONE: Record<string, string> = {
  primary: "text-primary",
  secondary: "text-secondary",
  tertiary: "text-tertiary",
  default: "text-ink-primary",
};

function Metric({
  label,
  value,
  tone = "default",
  live,
}: {
  label: string;
  value: string;
  tone?: keyof typeof TONE | "default";
  live?: boolean;
}) {
  return (
    <div className="flex flex-col leading-tight">
      <span className="flex items-center font-mono text-[9px] uppercase tracking-caps text-ink-faint">
        {live && <span className="live-dot mr-1 inline-block h-1.5 w-1.5 rounded-full bg-status-error" />}
        {label}
      </span>
      <span className={cx("font-display text-sm font-semibold tabular-nums md:text-base", TONE[tone])}>{value}</span>
    </div>
  );
}

/** Top-of-broadcast telemetry: viewers, thinking rate, tokens, prize, alive. */
export function MafiaLiveMetrics({
  prizePool,
  alive,
  className,
}: {
  prizePool: number;
  alive: number;
  className?: string;
}) {
  const watching = useDrift(13841, 50, 1900);
  const thinking = useDrift(742, 130, 1400);
  const tokens = useDrift(3_800_000, 45000, 1700);

  return (
    <div className={cx("flex flex-wrap items-center gap-x-6 gap-y-2", className)}>
      <Metric label="Watching" value={fmtNum(watching)} live />
      <Metric label="AI thinking/s" value={fmtNum(thinking)} tone="primary" />
      <Metric label="Tokens" value={`${(tokens / 1e6).toFixed(2)}M`} tone="tertiary" />
      <Metric label="Prize" value={`${fmtNum(prizePool)} CRD`} tone="secondary" />
      <Metric label="Alive" value={String(alive)} />
    </div>
  );
}

/** Town vs Mafia win-probability — updates as the board state changes. */
export function MafiaPredictionBar({
  townPct,
  className,
}: {
  townPct: number;
  className?: string;
}) {
  const t = Math.max(0, Math.min(100, Math.round(townPct)));
  const m = 100 - t;
  return (
    <div className={className}>
      <div className="mb-1.5 flex items-center justify-between font-mono text-[10px] uppercase tracking-caps">
        <span className="font-semibold text-primary">Town {t}%</span>
        <span className="text-ink-faint">Win prediction</span>
        <span className="font-semibold text-status-error">Mafia {m}%</span>
      </div>
      <div className="flex h-2.5 w-full overflow-hidden rounded-full border border-border-soft bg-bg-deep">
        <div className="h-full bg-primary transition-[width] duration-700 ease-out" style={{ width: `${t}%` }} />
        <div className="h-full bg-status-error transition-[width] duration-700 ease-out" style={{ width: `${m}%` }} />
      </div>
    </div>
  );
}
