// Minimal inline icon set (stroke-based, currentColor) — keeps the bundle light
// and matches the hairline "blueprint" aesthetic of the design system.
import * as React from "react";

type P = React.SVGProps<SVGSVGElement>;
const base = (props: P) => ({
  width: 18,
  height: 18,
  viewBox: "0 0 24 24",
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 1.6,
  strokeLinecap: "round" as const,
  strokeLinejoin: "round" as const,
  ...props,
});

export const Logo = ({ size = 22 }: { size?: number }) => (
  <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden>
    <path d="M12 2 3 7v10l9 5 9-5V7l-9-5Z" stroke="currentColor" strokeWidth="1.4" />
    <path d="M12 7v10M7.5 9.5 12 12l4.5-2.5" stroke="currentColor" strokeWidth="1.4" />
    <circle cx="12" cy="12" r="1.6" fill="currentColor" />
  </svg>
);

export const Bolt = (p: P) => (
  <svg {...base(p)}><path d="M13 2 4 14h7l-1 8 9-12h-7l1-8Z" /></svg>
);
export const Wallet = (p: P) => (
  <svg {...base(p)}><rect x="3" y="6" width="18" height="13" rx="2" /><path d="M3 10h18M16 13h2" /></svg>
);
export const Chart = (p: P) => (
  <svg {...base(p)}><path d="M4 20V10M10 20V4M16 20v-7M22 20H2" /></svg>
);
export const Gear = (p: P) => (
  <svg {...base(p)}><circle cx="12" cy="12" r="3" /><path d="M12 2v3M12 19v3M4.2 4.2l2.1 2.1M17.7 17.7l2.1 2.1M2 12h3M19 12h3M4.2 19.8l2.1-2.1M17.7 6.3l2.1-2.1" /></svg>
);
export const Trophy = (p: P) => (
  <svg {...base(p)}><path d="M6 4h12v3a6 6 0 0 1-12 0V4Z" /><path d="M6 5H3v1a3 3 0 0 0 3 3M18 5h3v1a3 3 0 0 1-3 3M9 17h6M10 17v3M14 17v3M8 22h8" /></svg>
);
export const Shield = (p: P) => (
  <svg {...base(p)}><path d="M12 3 5 6v5c0 4 3 7 7 9 4-2 7-5 7-9V6l-7-3Z" /><path d="M9 12l2 2 4-4" /></svg>
);
export const Lock = (p: P) => (
  <svg {...base(p)}><rect x="5" y="11" width="14" height="9" rx="1.5" /><path d="M8 11V8a4 4 0 0 1 8 0v3" /></svg>
);
export const Copy = (p: P) => (
  <svg {...base(p)}><rect x="9" y="9" width="11" height="11" rx="2" /><path d="M5 15V5a2 2 0 0 1 2-2h8" /></svg>
);
export const Check = (p: P) => (
  <svg {...base(p)}><path d="M4 12l5 5L20 6" /></svg>
);
export const Arrow = (p: P) => (
  <svg {...base(p)}><path d="M5 12h14M13 6l6 6-6 6" /></svg>
);
export const ChevronRight = (p: P) => (
  <svg {...base(p)}><path d="M9 6l6 6-6 6" /></svg>
);
export const ChevronLeft = (p: P) => (
  <svg {...base(p)}><path d="M15 6l-6 6 6 6" /></svg>
);
export const Coin = (p: P) => (
  <svg {...base(p)}><circle cx="12" cy="12" r="9" /><path d="M12 7v10M9.5 9.5h3.5a1.5 1.5 0 0 1 0 3H9.5h4" /></svg>
);
export const Home = (p: P) => (
  <svg {...base(p)}><path d="M4 11l8-7 8 7M6 10v9h12v-9" /></svg>
);
export const Eye = (p: P) => (
  <svg {...base(p)}><path d="M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12Z" /><circle cx="12" cy="12" r="2.5" /></svg>
);
export const X = (p: P) => (
  <svg {...base(p)}><path d="M6 6l12 12M18 6 6 18" /></svg>
);
export const Cpu = (p: P) => (
  <svg {...base(p)}><rect x="6" y="6" width="12" height="12" rx="2" /><rect x="9.5" y="9.5" width="5" height="5" rx="1" /><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3" /></svg>
);
export const Moon = (p: P) => (
  <svg {...base(p)}><path d="M20 14.5A8 8 0 0 1 9.5 4 8 8 0 1 0 20 14.5Z" /></svg>
);
export const Sun = (p: P) => (
  <svg {...base(p)}><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" /></svg>
);
export const Skull = (p: P) => (
  <svg {...base(p)}><path d="M12 3a8 8 0 0 0-5 14v3h10v-3a8 8 0 0 0-5-14Z" /><circle cx="9" cy="12" r="1.4" fill="currentColor" stroke="none" /><circle cx="15" cy="12" r="1.4" fill="currentColor" stroke="none" /><path d="M10.5 17h3" /></svg>
);
export const Gavel = (p: P) => (
  <svg {...base(p)}><path d="M14 4l6 6M11 7l6 6M9 9l-5 5 3 3 5-5M14.5 14.5l4 4M4 21h8" /></svg>
);
export const Search = (p: P) => (
  <svg {...base(p)}><circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" /></svg>
);
export const Users = (p: P) => (
  <svg {...base(p)}><circle cx="9" cy="8" r="3.2" /><path d="M3.5 20a5.5 5.5 0 0 1 11 0M16 5.2a3.2 3.2 0 0 1 0 6M17.5 20a5.5 5.5 0 0 0-3-4.9" /></svg>
);
export const Play = (p: P) => (
  <svg {...base(p)}><path d="M7 4.5v15l13-7.5-13-7.5Z" /></svg>
);
export const Pause = (p: P) => (
  <svg {...base(p)}><rect x="6.5" y="5" width="3.5" height="14" rx="1" /><rect x="14" y="5" width="3.5" height="14" rx="1" /></svg>
);
export const Cross = (p: P) => (
  <svg {...base(p)}><path d="M12 5v14M5 12h14" /></svg>
);
export const Crosshair = (p: P) => (
  <svg {...base(p)}><circle cx="12" cy="12" r="8" /><path d="M12 2v4M12 18v4M2 12h4M18 12h4" /></svg>
);
export const Restart = (p: P) => (
  <svg {...base(p)}><path d="M3 12a9 9 0 1 0 3-6.7M3 4v4h4" /></svg>
);
export const Brain = (p: P) => (
  <svg {...base(p)}><path d="M9 4a2.5 2.5 0 0 0-2.5 2.5A2.5 2.5 0 0 0 4 9c0 1 .5 1.8 1.2 2.3A2.5 2.5 0 0 0 4 13.5 2.5 2.5 0 0 0 6.5 16 2.5 2.5 0 0 0 9 18.5V4Z" /><path d="M15 4a2.5 2.5 0 0 1 2.5 2.5A2.5 2.5 0 0 1 20 9c0 1-.5 1.8-1.2 2.3A2.5 2.5 0 0 1 20 13.5 2.5 2.5 0 0 1 17.5 16 2.5 2.5 0 0 1 15 18.5V4Z" /></svg>
);
export const Layers = (p: P) => (
  <svg {...base(p)}><path d="M12 3 3 8l9 5 9-5-9-5Z" /><path d="M3 13l9 5 9-5M3 8" /></svg>
);
export const Flame = (p: P) => (
  <svg {...base(p)}><path d="M12 3c1 3-2 4-2 7a2 2 0 0 0 4 0c0-1 0-1.5-.3-2 1.8 1 3.3 2.9 3.3 5.5a5 5 0 0 1-10 0C7 12 9.5 9 12 3Z" /></svg>
);
export const Equals = (p: P) => (
  <svg {...base(p)}><path d="M5 9h14M5 15h14" /></svg>
);
