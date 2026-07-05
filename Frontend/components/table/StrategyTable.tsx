"use client";

// Onavion Strategy Table — the shared luxury surface every game is played on.
// A pure-SVG dark-walnut table (round · oval · square · rectangular) with layered
// 3D depth (top surface → bevel → lower edge → ambient + floor shadow), fine SVG
// wood grain, an above-lit center spotlight, and an optional "night" dimming.
// Scales infinitely, is theme-dark by nature, and accepts children rendered on top.

import * as React from "react";
import { cn } from "@/lib/cn";

export type TableShape = "round" | "oval" | "square" | "rectangular";

export function StrategyTable({
  shape = "round",
  size = 0.86,
  night = false,
  spotlight = true,
  topDown = false,
  className,
  children,
}: {
  shape?: TableShape;
  /** tabletop extent as a fraction of the SVG half-box (0–1). Smaller → agents/board sit further outside the rim. */
  size?: number;
  night?: boolean;
  spotlight?: boolean;
  /** symmetric lighting + ambient shadow, for tables viewed top-down with players all around (Mafia, Goofspiel). */
  topDown?: boolean;
  className?: string;
  children?: React.ReactNode;
}) {
  const uid = React.useId().replace(/[:]/g, "");
  const id = (s: string) => `st-${uid}-${s}`;

  const C = 500;
  const halfX = 500 * size;
  const halfY = shape === "oval" || shape === "rectangular" ? 500 * size * 0.72 : 500 * size;
  const bevel = Math.max(9, halfX * 0.042);
  const lift = topDown ? Math.max(4, halfY * 0.018) : Math.max(9, halfY * 0.05);

  // one shape family, four silhouettes — the shared visual language
  const shapeEl = (inset: number, props: React.SVGProps<SVGCircleElement & SVGRectElement & SVGEllipseElement>) => {
    const x = halfX - inset;
    const y = halfY - inset;
    if (shape === "round") return <circle cx={C} cy={C} r={x} {...(props as React.SVGProps<SVGCircleElement>)} />;
    if (shape === "oval") return <ellipse cx={C} cy={C} rx={x} ry={y} {...(props as React.SVGProps<SVGEllipseElement>)} />;
    const rr = (shape === "square" ? x * 0.2 : x * 0.12);
    return <rect x={C - x} y={C - y} width={2 * x} height={2 * y} rx={rr} ry={rr} {...(props as React.SVGProps<SVGRectElement>)} />;
  };

  return (
    <div className={cn("relative", className)}>
      <svg viewBox="0 0 1000 1000" preserveAspectRatio="xMidYMid meet" className="pointer-events-none absolute inset-0 h-full w-full" aria-hidden>
        <defs>
          {/* walnut top — near-uniform, only a whisper lighter in the center, darkening to the rim */}
          <radialGradient id={id("top")} cx="50%" cy={topDown ? "50%" : "44%"} r="64%">
            <stop offset="0%" stopColor="#2E231A" />
            <stop offset="55%" stopColor="#2B2118" />
            <stop offset="82%" stopColor="#241B15" />
            <stop offset="100%" stopColor="#16120E" />
          </radialGradient>
          {/* carved bevel */}
          <linearGradient id={id("bevel")} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="#4A392B" />
            <stop offset="16%" stopColor="#2B2118" />
            <stop offset="100%" stopColor="#16120E" />
          </linearGradient>
          {/* soft above spotlight — almost invisible, just enough to lift the center */}
          <radialGradient id={id("spot")} cx="50%" cy="48%" r="46%">
            <stop offset="0%" stopColor="#ffffff" stopOpacity="0.022" />
            <stop offset="60%" stopColor="#ffffff" stopOpacity="0.006" />
            <stop offset="100%" stopColor="#ffffff" stopOpacity="0" />
          </radialGradient>
          <radialGradient id={id("floor")} cx="50%" cy="50%" r="50%">
            <stop offset="0%" stopColor="#000000" stopOpacity="0.55" />
            <stop offset="100%" stopColor="#000000" stopOpacity="0" />
          </radialGradient>
          {/* recessed inner shadow near the rim */}
          <radialGradient id={id("inner")} cx="50%" cy="50%" r="50%">
            <stop offset="72%" stopColor="#000000" stopOpacity="0" />
            <stop offset="100%" stopColor="#000000" stopOpacity="0.5" />
          </radialGradient>
          {/* long, low-contrast wood grain (stretched fractal noise) */}
          <filter id={id("grain")}>
            <feTurbulence type="fractalNoise" baseFrequency="0.006 0.11" numOctaves="2" seed="11" stitchTiles="stitch" result="t" />
            <feColorMatrix in="t" type="saturate" values="0" />
          </filter>
          <filter id={id("blur")} x="-60%" y="-60%" width="220%" height="220%">
            <feGaussianBlur stdDeviation="30" />
          </filter>
          <clipPath id={id("clip")}>{shapeEl(bevel, {})}</clipPath>
        </defs>

        {/* ambient shadow — a symmetric ring for top-down tables, a floor pool otherwise */}
        {topDown
          ? shapeEl(-Math.max(14, halfX * 0.05), { fill: "#000000", opacity: 0.45, filter: `url(#${id("blur")})` })
          : <ellipse cx={C} cy={C + halfY * 0.52} rx={halfX * 1.02} ry={halfY * 0.32} fill={`url(#${id("floor")})`} filter={`url(#${id("blur")})`} />}
        {/* lower edge → ~5–8cm perceived thickness */}
        {shapeEl(0, { fill: "#16120E", transform: `translate(0 ${lift})` })}
        {/* carved bevel */}
        {shapeEl(0, { fill: `url(#${id("bevel")})` })}
        {/* top surface */}
        {shapeEl(bevel, { fill: `url(#${id("top")})` })}
        {/* wood grain, clipped to the top and blended in gently */}
        <g clipPath={`url(#${id("clip")})`} style={{ mixBlendMode: "overlay" }} opacity="0.06">
          <rect x="0" y="0" width="1000" height="1000" filter={`url(#${id("grain")})`} />
        </g>
        {/* recessed inner shadow */}
        {shapeEl(bevel, { fill: `url(#${id("inner")})` })}
        {/* fine edge highlight */}
        {shapeEl(bevel, { fill: "none", stroke: "#4A392B", strokeOpacity: 0.5, strokeWidth: 1.4 })}
        {/* center spotlight — almost invisible, draws the eye */}
        {spotlight && shapeEl(bevel + 4, { fill: `url(#${id("spot")})`, className: "st-breathe" })}
        {/* night dimming */}
        {night && shapeEl(bevel, { fill: "#05050a", opacity: 0.42, style: { transition: "opacity .45s ease" } })}
      </svg>
      {children}
    </div>
  );
}
