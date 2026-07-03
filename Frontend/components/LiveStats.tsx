"use client";

import { useEffect, useState } from "react";
import { fmt } from "@/lib/mock";
import { cx } from "@/components/ui";

// LiveStats renders the hero HUD numbers with a count-up animation, a staggered
// entrance, a moving sheen on the accent bar and a breathing corner glow — so the
// arena feels alive the moment the page loads. Data comes from the server page as
// plain numbers (RSC-serializable).
export interface LiveStat {
  label: string;
  value: number;
  accent: string;
  tone?: "teal" | "amber";
  formatted?: boolean; // apply fmt() (K/M abbreviations)
}

function useCountUp(value: number, dur = 1200) {
  const [n, setN] = useState(0);
  useEffect(() => {
    let raf = 0;
    const start = performance.now();
    const tick = (t: number) => {
      const p = Math.min(1, (t - start) / dur);
      const eased = 1 - Math.pow(1 - p, 3); // easeOutCubic
      setN(Math.round(value * eased));
      if (p < 1) raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [value, dur]);
  return n;
}

function StatCard({ s, i }: { s: LiveStat; i: number }) {
  const n = useCountUp(s.value);
  const display = s.formatted ? fmt(n) : String(n);
  return (
    <div
      className="group relative overflow-hidden rounded-xl border border-border-strong bg-surface-slate/70 px-5 py-5 text-center backdrop-blur-sm transition-transform duration-300 hover:-translate-y-1"
      style={{
        boxShadow: `inset 0 0 26px ${s.accent}1f`,
        animation: `statIn .6s ${i * 0.12}s both cubic-bezier(.2,.7,.2,1)`,
      }}
    >
      {/* accent bar + moving sheen */}
      <span aria-hidden className="absolute inset-x-0 top-0 h-[3px]" style={{ background: s.accent }} />
      <span
        aria-hidden
        className="absolute top-0 h-[3px] w-1/3"
        style={{
          background: `linear-gradient(90deg, transparent, #ffffffcc, transparent)`,
          animation: `statSheen 2.8s ${i * 0.3}s linear infinite`,
        }}
      />
      {/* breathing corner glow */}
      <span
        aria-hidden
        className="pointer-events-none absolute -right-8 -top-8 -z-10 h-24 w-24 animate-pulse rounded-full blur-2xl"
        style={{ background: `radial-gradient(circle, ${s.accent}55, transparent 70%)` }}
      />
      <div
        className={cx(
          "font-mono text-3xl font-bold tabular-nums md:text-[34px]",
          s.tone === "teal" ? "text-primary" : s.tone === "amber" ? "text-secondary" : "text-ink-primary",
        )}
        style={{ textShadow: `0 0 22px ${s.accent}33` }}
      >
        {display}
      </div>
      <div className="mt-1.5 label-caps">{s.label}</div>
    </div>
  );
}

export function LiveStats({ stats }: { stats: LiveStat[] }) {
  return (
    <div className="grid grid-cols-3 gap-3">
      {stats.map((s, i) => (
        <StatCard key={s.label} s={s} i={i} />
      ))}
      <style>{`
        @keyframes statIn { from { opacity: 0; transform: translateY(12px) } to { opacity: 1; transform: none } }
        @keyframes statSheen { 0% { left: -35% } 100% { left: 100% } }
      `}</style>
    </div>
  );
}
