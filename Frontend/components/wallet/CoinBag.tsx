"use client";

import * as React from "react";
import { cn } from "@/lib/cn";

// CoinBag — a real circular gold coin + a money bag that holds the balance.
// When the balance changes, coins visibly FLOW: into the bag on a gain (buy /
// win / reward) and out of the bag on a spend (stake / loss / cash-out). The
// number tweens to the new value and the bag flashes green (gain) or red (loss).
// Pure presentational + self-contained animation; feed it `balance`.

/** A single gold coin — a real circular coin with rim, face, shine and a ◆ mark. */
export function GoldCoin({ size = 22, className }: { size?: number; className?: string }) {
  const id = React.useId().replace(/:/g, "");
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" className={className} aria-hidden>
      <defs>
        <radialGradient id={`face-${id}`} cx="38%" cy="32%" r="75%">
          <stop offset="0%" stopColor="#fff3c4" />
          <stop offset="52%" stopColor="#f7cf5b" />
          <stop offset="100%" stopColor="#e0a422" />
        </radialGradient>
        <linearGradient id={`rim-${id}`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="#f6d271" />
          <stop offset="100%" stopColor="#b9791a" />
        </linearGradient>
      </defs>
      {/* rim */}
      <circle cx="12" cy="12" r="11" fill={`url(#rim-${id})`} />
      {/* inset ring */}
      <circle cx="12" cy="12" r="9.2" fill="none" stroke="#a86c14" strokeWidth="0.6" opacity="0.5" />
      {/* face */}
      <circle cx="12" cy="12" r="8.6" fill={`url(#face-${id})`} />
      {/* ◆ brand mark */}
      <path d="M12 7.4 15 12l-3 4.6L9 12z" fill="#9a6410" opacity="0.85" />
      <path d="M12 8.6 13.9 12 12 15.4 10.1 12z" fill="#fff0bf" opacity="0.7" />
      {/* top-left shine */}
      <ellipse cx="9" cy="8.4" rx="3.1" ry="1.9" fill="#ffffff" opacity="0.45" transform="rotate(-32 9 8.4)" />
    </svg>
  );
}

/** The money bag (pouch) the coins live in. `tone` drives a brief glow flash. */
function MoneyBag({ tone, coinSize = 128 }: { tone: "gain" | "loss" | null; coinSize?: number }) {
  const id = React.useId().replace(/:/g, "");
  return (
    <svg
      width={coinSize}
      height={coinSize}
      viewBox="0 0 64 64"
      className={cn("cb-bag", tone === "gain" && "cb-bag-gain", tone === "loss" && "cb-bag-loss")}
      aria-hidden
    >
      <defs>
        <linearGradient id={`bag-${id}`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="#3a2f5f" />
          <stop offset="46%" stopColor="#2a2545" />
          <stop offset="100%" stopColor="#1a1730" />
        </linearGradient>
        <radialGradient id={`sheen-${id}`} cx="38%" cy="30%" r="70%">
          <stop offset="0%" stopColor="#ffffff" stopOpacity="0.16" />
          <stop offset="60%" stopColor="#ffffff" stopOpacity="0" />
        </radialGradient>
      </defs>
      {/* body */}
      <path
        d="M22 21c-6 4-11 12-11 21 0 9 8 14 21 14s21-5 21-14c0-9-5-17-11-21z"
        fill={`url(#bag-${id})`}
        stroke="#6c5cc4"
        strokeWidth="1.2"
      />
      <path
        d="M22 21c-6 4-11 12-11 21 0 9 8 14 21 14s21-5 21-14c0-9-5-17-11-21z"
        fill={`url(#sheen-${id})`}
      />
      {/* cinched neck + drawstring */}
      <path d="M22 21l3-7h14l3 7c-3 2-7 3-10 3s-7-1-10-3z" fill="#2a2545" stroke="#6c5cc4" strokeWidth="1.2" />
      <path d="M21 21c4 3 18 3 22 0" fill="none" stroke="#e0a422" strokeWidth="2" strokeLinecap="round" />
      <path d="M25 14h14" stroke="#8a78e0" strokeWidth="1.4" strokeLinecap="round" />
      {/* ◆ emblem */}
      <path d="M32 33l6 9-6 9-6-9z" fill="#e0a422" opacity="0.92" />
      <path d="M32 36l3.6 6-3.6 6-3.6-6z" fill="#fff0bf" opacity="0.35" />
    </svg>
  );
}

type Flow = { id: number; dir: "in" | "out"; dx: number; delay: number };

export interface CoinBagProps {
  /** Current balance in coins. Undefined = loading. */
  balance?: number;
  currency?: string;
  /** Small caption under the number. */
  subtitle?: React.ReactNode;
  className?: string;
  /** Bag pixel size. */
  bagSize?: number;
}

export function CoinBag({ balance, currency = "CRD", subtitle, className, bagSize = 128 }: CoinBagProps) {
  const prev = React.useRef<number | null>(null);
  const displayRef = React.useRef(0);
  const [display, setDisplay] = React.useState(0);
  const [flows, setFlows] = React.useState<Flow[]>([]);
  const [tone, setTone] = React.useState<"gain" | "loss" | null>(null);
  const [delta, setDelta] = React.useState<number | null>(null);
  const nextId = React.useRef(0);

  React.useEffect(() => {
    if (balance == null) return;
    const from = displayRef.current;
    const to = balance;
    const change = prev.current == null ? 0 : to - prev.current;
    let flashTimer: ReturnType<typeof setTimeout> | undefined;

    // On any real change (and on the first load, as a "fill" entrance) send coins
    // flowing the right way and flash the bag.
    if (prev.current == null || change !== 0) {
      const dir: "in" | "out" = change < 0 ? "out" : "in";
      const magnitude = prev.current == null ? Math.max(1, to) : Math.abs(change);
      spawn(dir, magnitude);
      if (prev.current != null) {
        setTone(change > 0 ? "gain" : "loss");
        setDelta(change);
        flashTimer = setTimeout(() => {
          setTone(null);
          setDelta(null);
        }, 1100);
      }
    }
    prev.current = to;

    // Tween the displayed number from -> to.
    const start = performance.now();
    const dur = 720;
    let raf = 0;
    const tick = (now: number) => {
      const p = Math.min(1, (now - start) / dur);
      const eased = 1 - Math.pow(1 - p, 3);
      const val = Math.round(from + (to - from) * eased);
      displayRef.current = val;
      setDisplay(val);
      if (p < 1) raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => {
      cancelAnimationFrame(raf);
      if (flashTimer) clearTimeout(flashTimer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [balance]);

  function spawn(dir: "in" | "out", magnitude: number) {
    const n = Math.min(10, Math.max(4, Math.round(Math.log10(magnitude + 1) * 3.2)));
    const burst: Flow[] = Array.from({ length: n }, () => ({
      id: nextId.current++,
      dir,
      dx: Math.round((Math.random() * 2 - 1) * 42),
      delay: Math.round(Math.random() * 280),
    }));
    setFlows((f) => [...f, ...burst]);
  }

  const loading = balance == null;

  return (
    <div className={cn("flex items-center gap-5", className)}>
      {/* Bag + coin-flow overlay */}
      <div className="relative shrink-0" style={{ width: bagSize, height: bagSize }}>
        <MoneyBag tone={tone} coinSize={bagSize} />
        <div className="pointer-events-none absolute inset-0 overflow-visible">
          {flows.map((f) => (
            <span
              key={f.id}
              className={cn("cb-flow", f.dir === "in" ? "cb-flow-in" : "cb-flow-out")}
              style={
                {
                  "--cb-dx": `${f.dx}px`,
                  animationDelay: `${f.delay}ms`,
                } as React.CSSProperties
              }
              onAnimationEnd={() => setFlows((cur) => cur.filter((x) => x.id !== f.id))}
            >
              <GoldCoin size={Math.round(bagSize * 0.17)} />
            </span>
          ))}
        </div>
      </div>

      {/* Balance readout */}
      <div className="min-w-0">
        <div className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">Coin balance</div>
        <div className="mt-1 flex items-center gap-2">
          <GoldCoin size={26} />
          <span className="font-mono text-3xl font-semibold tabular-nums text-fg">
            {loading ? "…" : display.toLocaleString()}
          </span>
          <span className="font-mono text-sm text-fg-muted">{currency}</span>
          {delta != null && delta !== 0 && (
            <span
              key={delta}
              className={cn(
                "cb-delta font-mono text-sm font-semibold",
                delta > 0 ? "text-ok" : "text-danger",
              )}
            >
              {delta > 0 ? `+${delta.toLocaleString()}` : delta.toLocaleString()}
            </span>
          )}
        </div>
        {subtitle && <div className="mt-1 font-mono text-[11px] text-fg-muted">{subtitle}</div>}
      </div>

      <style jsx global>{`
        .cb-bag {
          filter: drop-shadow(0 10px 22px rgba(0, 0, 0, 0.45));
          transition: filter 0.3s ease, transform 0.3s ease;
        }
        .cb-bag-gain {
          animation: cb-bag-bounce 0.6s cubic-bezier(0.2, 0.8, 0.2, 1);
          filter: drop-shadow(0 0 18px rgba(52, 211, 153, 0.55));
        }
        .cb-bag-loss {
          animation: cb-bag-shake 0.5s ease;
          filter: drop-shadow(0 0 18px rgba(239, 68, 68, 0.5));
        }
        @keyframes cb-bag-bounce {
          0%, 100% { transform: translateY(0) scale(1); }
          35% { transform: translateY(3px) scale(1.05, 0.95); }
          70% { transform: translateY(-2px) scale(0.98, 1.02); }
        }
        @keyframes cb-bag-shake {
          0%, 100% { transform: translateX(0); }
          25% { transform: translateX(-2px); }
          75% { transform: translateX(2px); }
        }
        /* A flowing coin sits at the bag's neck (top-center) and travels. */
        .cb-flow {
          position: absolute;
          left: 50%;
          top: 14%;
          margin-left: -9px;
          will-change: transform, opacity;
        }
        .cb-flow-in {
          animation: cb-in 0.85s cubic-bezier(0.35, 0.9, 0.4, 1) forwards;
        }
        .cb-flow-out {
          animation: cb-out 0.85s cubic-bezier(0.4, 0, 0.6, 1) forwards;
        }
        /* IN: drop from above (offset by --cb-dx), fall into the neck, vanish. */
        @keyframes cb-in {
          0% { transform: translate(var(--cb-dx), -64px) scale(0.7); opacity: 0; }
          25% { opacity: 1; }
          70% { transform: translate(calc(var(--cb-dx) * 0.2), 0) scale(1); opacity: 1; }
          100% { transform: translate(0, 18px) scale(0.55); opacity: 0; }
        }
        /* OUT: rise from the neck outward toward --cb-dx and fade. */
        @keyframes cb-out {
          0% { transform: translate(0, 12px) scale(0.6); opacity: 0; }
          30% { transform: translate(calc(var(--cb-dx) * 0.3), -6px) scale(1); opacity: 1; }
          100% { transform: translate(var(--cb-dx), -66px) scale(0.7); opacity: 0; }
        }
        .cb-delta {
          animation: cb-delta 1.1s ease forwards;
        }
        @keyframes cb-delta {
          0% { transform: translateY(6px); opacity: 0; }
          20% { transform: translateY(0); opacity: 1; }
          80% { opacity: 1; }
          100% { transform: translateY(-8px); opacity: 0; }
        }
        @media (prefers-reduced-motion: reduce) {
          .cb-flow, .cb-bag-gain, .cb-bag-loss, .cb-delta { animation: none; }
          .cb-flow { display: none; }
        }
      `}</style>
    </div>
  );
}
