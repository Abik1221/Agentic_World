"use client";

import { cx } from "@/components/ui";
import type { AgentPersonality, AgentProfile, AgentStatus } from "@/lib/agentIdentity";

const ACCENT_RING: Record<AgentProfile["accent"], string> = {
  teal: "ring-primary/60 shadow-[0_0_20px_rgba(67,230,201,0.35)]",
  blue: "ring-tertiary/60 shadow-[0_0_20px_rgba(169,199,255,0.35)]",
  amber: "ring-secondary/60 shadow-[0_0_20px_rgba(240,192,77,0.35)]",
  red: "ring-status-error/50 shadow-[0_0_20px_rgba(255,107,129,0.3)]",
  violet: "ring-violet-400/50 shadow-[0_0_20px_rgba(167,139,250,0.3)]",
  rose: "ring-rose-400/50 shadow-[0_0_20px_rgba(251,113,133,0.3)]",
};

const ACCENT_BG: Record<AgentProfile["accent"], string> = {
  teal: "from-primary/30 to-primary-container/10",
  blue: "from-tertiary/30 to-tertiary/5",
  amber: "from-secondary/30 to-secondary/5",
  red: "from-status-error/25 to-status-error/5",
  violet: "from-violet-500/25 to-violet-500/5",
  rose: "from-rose-500/25 to-rose-500/5",
};

const PERSONALITY_MARK: Record<AgentPersonality, string> = {
  Strategist: "◈",
  Detective: "◎",
  Hacker: "⬡",
  Commander: "▣",
  Scientist: "⚗",
  Guardian: "⛨",
  Shadow: "◐",
  Aggressive: "⚡",
};

export function AgentAvatar({
  profile,
  size = "md",
  status = "alive",
  speaking,
  voting,
  dead,
  imageUrl,
  className,
}: {
  profile: Pick<AgentProfile, "id" | "name" | "accent" | "personality">;
  size?: "sm" | "md" | "lg" | "xl";
  status?: AgentStatus;
  speaking?: boolean;
  voting?: boolean;
  dead?: boolean;
  /** When set, render the agent's uploaded photo instead of the generated face. */
  imageUrl?: string;
  className?: string;
}) {
  const dim = { sm: 36, md: 48, lg: 64, xl: 88 }[size];
  const glow = speaking || status === "speaking";
  const pulse = voting || status === "voting";
  const idle = (size === "lg" || size === "xl") && !dead && !pulse;

  return (
    <div
      className={cx(
        "relative shrink-0 rounded-2xl bg-gradient-to-br p-[2px] transition-all duration-300",
        ACCENT_BG[profile.accent],
        dead && "opacity-45 grayscale",
        glow && `ring-2 ${ACCENT_RING[profile.accent]}`,
        pulse && "animate-vote-pulse",
        idle && "mf-breathe",
        className,
      )}
      style={{ width: dim, height: dim, animationDelay: idle ? `${(profile.id % 5) * 0.5}s` : undefined }}
      title={profile.name}
    >
      {imageUrl ? (
        // eslint-disable-next-line @next/next/no-img-element
        <img
          src={imageUrl}
          alt={profile.name}
          className="h-full w-full rounded-[14px] object-cover"
          draggable={false}
        />
      ) : (
      <svg viewBox="0 0 64 64" className="h-full w-full rounded-[14px] bg-[#241f1a]" aria-hidden>
        <defs>
          <linearGradient id={`av-${profile.id}`} x1="0" y1="0" x2="1" y2="1">
            <stop offset="0%" stopColor="rgba(140,255,230,0.34)" />
            <stop offset="100%" stopColor="rgba(169,199,255,0.12)" />
          </linearGradient>
        </defs>
        <rect width="64" height="64" fill={`url(#av-${profile.id})`} rx="14" />
        <ellipse cx="32" cy="28" rx="18" ry="14" fill="rgba(255,255,255,0.06)" stroke="rgba(255,255,255,0.12)" />
        <g className={idle ? "mf-eyes" : undefined} style={idle ? { animationDelay: `${(profile.id % 7) * 0.8}s` } : undefined}>
          <rect x="18" y="24" width="10" height="6" rx="2" fill="rgba(140,255,230,0.5)" />
          <rect x="36" y="24" width="10" height="6" rx="2" fill="rgba(140,255,230,0.5)" />
        </g>
        <path d="M22 44 Q32 52 42 44" stroke="rgba(255,255,255,0.15)" fill="none" strokeWidth="2" />
        <text x="48" y="14" fontSize="10" fill="rgba(255,255,255,0.7)" textAnchor="middle">
          {PERSONALITY_MARK[profile.personality]}
        </text>
      </svg>
      )}
      {speaking && (
        <span className="absolute -bottom-1 left-1/2 -translate-x-1/2 rounded-full bg-primary px-1.5 py-0.5 font-mono text-[8px] uppercase tracking-wider text-on-primary">
          Live
        </span>
      )}
    </div>
  );
}
