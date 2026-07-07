"use client";

import * as React from "react";
import { Modal } from "@/components/Modal";
import { cx } from "@/components/ui";

// GameOverModal — the post-match results popup. Shows final standings (rank,
// avatar, name, outcome + the round a player was eliminated). The standings list
// lives in a FIXED-HEIGHT scroll area (chat-UI style): the modal frame never
// grows vertically, so a 12-seat Mafia table scrolls inside instead of pushing
// the popup off-screen. Game-agnostic — feed it a Standing[].

export interface Standing {
  key: string;
  rank: number;
  name: string;
  avatar?: string; // image URL; falls back to colored initials
  color?: string; // avatar tint when no image
  seat?: number;
  outcome: "winner" | "survived" | "eliminated";
  /** Right-side detail, e.g. "Eliminated · Day 2" or "Survived". */
  detail?: string;
  /** Under the name, e.g. "Mafia · gpt-5". */
  sub?: string;
  you?: boolean;
}

const OUTCOME = {
  winner: { label: "Winner", cls: "text-secondary border-secondary/40 bg-secondary/10" },
  survived: { label: "Survived", cls: "text-primary border-primary/40 bg-primary/10" },
  eliminated: { label: "Out", cls: "text-status-error border-status-error/40 bg-status-error/10" },
} as const;

export function GameOverModal({
  open,
  onClose,
  title = "Game Over",
  banner,
  standings,
  footer,
}: {
  open: boolean;
  onClose: () => void;
  title?: React.ReactNode;
  banner?: React.ReactNode;
  standings: Standing[];
  footer?: React.ReactNode;
}) {
  return (
    <Modal open={open} onClose={onClose} eyebrow="Match result" title={title} size="md" footer={footer}>
      {banner && (
        <div className="mb-4 rounded-lg border border-secondary/30 bg-secondary/10 px-4 py-3 text-center font-display text-base font-semibold text-secondary">
          {banner}
        </div>
      )}

      <div className="mb-2 flex items-center justify-between font-mono text-[10px] uppercase tracking-caps text-ink-faint">
        <span>Final standings</span>
        <span>{standings.length} players</span>
      </div>

      {/* Fixed-height scroll area — the frame stays put; only this list scrolls. */}
      <div className="max-h-[46vh] space-y-1.5 overflow-y-auto pr-1">
        {standings.map((s) => (
          <StandingRow key={s.key} s={s} />
        ))}
      </div>
    </Modal>
  );
}

function StandingRow({ s }: { s: Standing }) {
  const oc = OUTCOME[s.outcome];
  return (
    <div
      className={cx(
        "flex shrink-0 items-center gap-3 rounded-lg border px-3 py-2.5",
        s.you ? "border-primary/50 bg-primary/[0.06]" : "border-border-soft bg-surface-high/30",
      )}
    >
      <RankPip rank={s.rank} />
      <Avatar name={s.name} avatar={s.avatar} color={s.color} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <span className="truncate text-sm font-medium text-ink-primary">{s.name}</span>
          {s.you && (
            <span className="rounded-full bg-primary/20 px-1.5 py-0.5 font-mono text-[9px] uppercase tracking-caps text-primary">
              you
            </span>
          )}
        </div>
        {s.sub && <div className="truncate font-mono text-[11px] text-ink-faint">{s.sub}</div>}
      </div>
      <div className="flex flex-col items-end gap-1">
        <span className={cx("rounded-full border px-2 py-0.5 font-mono text-[10px] uppercase tracking-caps", oc.cls)}>
          {oc.label}
        </span>
        {s.detail && <span className="font-mono text-[11px] text-ink-dim">{s.detail}</span>}
      </div>
    </div>
  );
}

function RankPip({ rank }: { rank: number }) {
  const medal = rank <= 3;
  return (
    <span
      className={cx(
        "flex h-6 w-6 shrink-0 items-center justify-center rounded-full font-mono text-[11px] font-bold",
        rank === 1 && "bg-secondary/20 text-secondary",
        rank === 2 && "bg-ink-faint/20 text-ink-primary",
        rank === 3 && "bg-primary/15 text-primary",
        !medal && "text-ink-faint",
      )}
    >
      {rank}
    </span>
  );
}

function Avatar({ name, avatar, color }: { name: string; avatar?: string; color?: string }) {
  if (avatar) {
    // eslint-disable-next-line @next/next/no-img-element
    return <img src={avatar} alt="" className="h-8 w-8 shrink-0 rounded-full object-cover" />;
  }
  const initials = name.replace(/[^A-Za-z0-9]/g, "").slice(0, 2).toUpperCase() || "AI";
  return (
    <span
      className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full font-mono text-[11px] font-bold text-white"
      style={{ backgroundColor: color || "rgb(var(--c-primary))" }}
    >
      {initials}
    </span>
  );
}
