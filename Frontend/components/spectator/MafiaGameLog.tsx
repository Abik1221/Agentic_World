"use client";

import { useEffect, useRef } from "react";
import { AgentAvatar } from "./AgentAvatar";
import { profileFor } from "@/lib/agentIdentity";
import { Pill, SectionLabel, cx } from "@/components/ui";
import type { MafiaChatMessage } from "./MafiaLiveChat";

/** Cinematic timeline log — not a chat. Each event reads like F1/esports telemetry:
 *  timestamp, agent, intent tag, a confidence bar, and streamed reasoning text. */

const TONE: Record<string, { label: string; text: string; bar: string; conf: number }> = {
  accuse: { label: "Accusation", text: "text-status-error", bar: "bg-status-error", conf: 91 },
  defend: { label: "Defense", text: "text-primary", bar: "bg-primary", conf: 74 },
  claim: { label: "Claim", text: "text-secondary", bar: "bg-secondary", conf: 81 },
  info: { label: "Read", text: "text-tertiary", bar: "bg-tertiary", conf: 86 },
  alliance: { label: "Alliance", text: "text-primary", bar: "bg-primary", conf: 78 },
};

function confidenceFor(m: MafiaChatMessage): { pct: number; tone: (typeof TONE)[string] } | null {
  if (!m.tone) return null;
  const tone = TONE[m.tone];
  if (!tone) return null;
  // small deterministic jitter so values feel computed, not fixed
  const seed = (m.from ?? 0) * 13 + m.text.length;
  const pct = Math.max(55, Math.min(98, tone.conf + ((seed % 9) - 4)));
  return { pct, tone };
}

function Divider({ text }: { text: string }) {
  const voting = /vot/i.test(text);
  const night = /night/i.test(text);
  return (
    <div className="tl-entry my-2 flex items-center gap-2">
      <span className="h-px flex-1 bg-border-soft" />
      <span
        className={cx(
          "rounded-full border px-2.5 py-0.5 text-center font-mono text-[9px] uppercase tracking-caps",
          voting
            ? "border-status-error/40 text-status-error"
            : night
              ? "border-tertiary/40 text-tertiary"
              : "border-border-strong text-ink-faint",
        )}
      >
        {text}
      </span>
      <span className="h-px flex-1 bg-border-soft" />
    </div>
  );
}

function Entry({ m, active }: { m: MafiaChatMessage; active: boolean }) {
  const profile = m.from != null ? profileFor(m.from, m.fromName ?? `Agent ${m.from}`) : null;
  const conf = confidenceFor(m);
  return (
    <div className="tl-entry relative pl-3">
      {/* timeline rail */}
      <span className="absolute bottom-0 left-0 top-2 w-px bg-border-soft" aria-hidden />
      <span
        className={cx(
          "absolute -left-[3px] top-2 h-[7px] w-[7px] rounded-full ring-2 ring-surface-slate",
          active ? "bg-primary-container" : conf ? conf.tone.bar : "bg-ink-faint",
        )}
        aria-hidden
      />
      <div
        className={cx(
          "ml-2 rounded-lg border p-3 transition",
          active
            ? "border-primary-container/60 bg-primary-container/[0.06] shadow-glow-teal"
            : "border-border-soft bg-surface-slate/60",
        )}
      >
        <div className="flex items-center gap-2">
          {profile && <AgentAvatar profile={profile} size="sm" speaking={active} />}
          <div className="min-w-0">
            <div className="truncate font-display text-[12px] font-semibold text-ink-primary">
              {m.fromName ?? `Agent ${m.from}`}
            </div>
            <div className="font-mono text-[9px] text-ink-faint">{m.timestamp}</div>
          </div>
          {conf && (
            <span className={cx("ml-auto shrink-0 font-mono text-[9px] uppercase tracking-caps", conf.tone.text)}>
              {conf.tone.label}
              {m.targetName ? ` → ${m.targetName.split("_")[0]}` : ""}
            </span>
          )}
        </div>

        {conf && (
          <div className="mt-2">
            <div className="flex items-center justify-between font-mono text-[9px] text-ink-faint">
              <span>{active ? "Analyzing…" : "Confidence"}</span>
              <span className={conf.tone.text}>{conf.pct}%</span>
            </div>
            <div className="mt-1 h-1 overflow-hidden rounded-full bg-bg-deep">
              <div className={cx("conf-fill h-full rounded-full", conf.tone.bar)} style={{ width: `${conf.pct}%` }} />
            </div>
          </div>
        )}

        <p className="tl-stream mt-2 font-mono text-[12px] leading-[1.55] text-ink-dim">{m.text}</p>
      </div>
    </div>
  );
}

export function MafiaGameLog({
  messages,
  activeSpeakerId,
  asleep,
  className,
}: {
  messages: MafiaChatMessage[];
  activeSpeakerId?: number | null;
  asleep?: boolean;
  className?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
  }, [messages.length]);

  const lastIdx = messages.length - 1;

  return (
    <div className={cx("flex h-full min-h-0 flex-col", className)}>
      <div className="flex items-center justify-between border-b border-border-soft px-4 py-3">
        <SectionLabel className="text-primary">◷ Game log</SectionLabel>
        {asleep ? (
          <Pill tone="blue" dot>
            Agents asleep
          </Pill>
        ) : (
          <Pill tone="teal" dot>
            Live timeline
          </Pill>
        )}
      </div>
      <div ref={ref} className="mafia-chat-scroll flex-1 space-y-2 overflow-y-auto px-3 py-3">
        {messages.length === 0 && (
          <p className="px-1 font-mono text-[12px] text-ink-faint">The first night unfolds in silence…</p>
        )}
        {messages.map((m, i) =>
          m.moderator ? (
            <Divider key={m.id} text={m.text} />
          ) : (
            <Entry key={m.id} m={m} active={m.from === activeSpeakerId && i === lastIdx && !asleep} />
          ),
        )}
      </div>
    </div>
  );
}
