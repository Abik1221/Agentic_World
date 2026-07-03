"use client";

// LiveTicker is a seamless, infinitely-scrolling marquee of what's happening in
// the arena right now. Items are built server-side from the live match feed and
// passed in as plain strings (RSC-serializable). The row is duplicated and
// translated -50% so the loop is seamless; edges fade out via a mask.
export function LiveTicker({ items }: { items: { game: string; text: string; accent: string }[] }) {
  if (items.length === 0) return null;
  const row = [...items, ...items];
  return (
    <div className="relative mt-4 overflow-hidden rounded-xl border border-border-soft bg-surface-slate/50 py-2.5">
      <div
        aria-hidden
        className="pointer-events-none absolute inset-y-0 left-0 z-10 w-16"
        style={{ background: "linear-gradient(90deg, var(--ticker-fade, rgba(0,0,0,0)), transparent)" }}
      />
      <div className="lt-track flex w-max items-center gap-8 pl-8">
        {row.map((it, i) => (
          <span key={i} className="flex items-center gap-2 whitespace-nowrap font-mono text-[11px] uppercase tracking-caps text-ink-dim">
            <span className="h-1.5 w-1.5 rounded-full" style={{ background: it.accent }} />
            <span className="font-semibold" style={{ color: it.accent }}>{it.game}</span>
            <span className="text-ink-faint">·</span>
            {it.text}
          </span>
        ))}
      </div>
      <style>{`
        .lt-track { animation: ltScroll 38s linear infinite; }
        .lt-track:hover { animation-play-state: paused; }
        @keyframes ltScroll { from { transform: translateX(0) } to { transform: translateX(-50%) } }
        @media (prefers-reduced-motion: reduce) { .lt-track { animation: none } }
      `}</style>
    </div>
  );
}
