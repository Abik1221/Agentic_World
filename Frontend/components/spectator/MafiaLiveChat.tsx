"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { AgentAvatar } from "./AgentAvatar";
import { profileFor } from "@/lib/agentIdentity";
import { SectionLabel, cx } from "@/components/ui";

export interface MafiaChatMessage {
  id: string;
  from?: number;
  fromName?: string;
  text: string;
  tone?: "accuse" | "defend" | "claim" | "info" | "alliance" | string;
  target?: number;
  targetName?: string;
  moderator?: boolean;
  timestamp?: string;
}

const TONE_STYLE: Record<string, string> = {
  ACCUSE: "border-status-error/30 bg-status-error/5 text-status-error",
  DEFEND: "border-primary/30 bg-primary/5 text-primary",
  CLAIM: "border-secondary/30 bg-secondary/5 text-secondary",
  READ: "border-tertiary/30 bg-tertiary/5 text-tertiary",
  ALLY: "border-primary/30 bg-primary/5 text-primary",
  accuse: "border-status-error/30 bg-status-error/5 text-status-error",
  defend: "border-primary/30 bg-primary/5 text-primary",
  claim: "border-secondary/30 bg-secondary/5 text-secondary",
  info: "border-tertiary/30 bg-tertiary/5 text-tertiary",
  alliance: "border-primary/30 bg-primary/5 text-primary",
};

const PIN_THRESHOLD_PX = 56;

/** Compact scrollable chat rail — scroll inside the panel only, auto-follow when pinned. */
export function MafiaLiveChat({
  messages,
  activeSpeakerId,
  asleep,
  thinkingAgentId,
  showRoles,
  className,
}: {
  messages: MafiaChatMessage[];
  activeSpeakerId?: number | null;
  asleep?: boolean;
  thinkingAgentId?: number | null;
  showRoles?: boolean;
  className?: string;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const [pinned, setPinned] = useState(true);

  const scrollToLatest = useCallback((behavior: ScrollBehavior = "auto") => {
    const el = scrollRef.current;
    if (!el) return;
    const top = el.scrollHeight - el.clientHeight;
    if (behavior === "smooth") {
      el.scrollTo({ top, behavior: "smooth" });
    } else {
      el.scrollTop = top;
    }
  }, []);

  const syncPinned = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
    setPinned(distanceFromBottom <= PIN_THRESHOLD_PX);
  }, []);

  useEffect(() => {
    if (!pinned) return;
    scrollToLatest(messages.length <= 2 ? "auto" : "smooth");
  }, [messages, messages.length, thinkingAgentId, activeSpeakerId, pinned, scrollToLatest]);

  useEffect(() => {
    const el = scrollRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;

    const observer = new ResizeObserver(() => {
      if (pinned) scrollToLatest("auto");
      else syncPinned();
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, [pinned, scrollToLatest, syncPinned]);

  return (
    <div className={cx("flex h-full min-h-0 flex-col overflow-hidden", className)}>
      <div className="shrink-0 border-b border-border-soft px-2.5 py-2 md:px-3">
        <div className="flex items-center justify-between gap-2">
          <SectionLabel className="text-[10px] text-tertiary">Live chat</SectionLabel>
          <span className="font-mono text-[9px] text-ink-faint">{messages.length}</span>
        </div>
      </div>

      <div className="relative min-h-0 flex-1 overflow-hidden">
        <div
          ref={scrollRef}
          onScroll={syncPinned}
          className="mafia-chat-scroll h-full space-y-2 overflow-y-auto overscroll-y-contain px-2.5 py-2 md:px-3"
        >
          {asleep && (
            <div className="rounded-lg border border-tertiary/30 bg-tertiary/10 px-3 py-5 text-center">
              <p className="font-display text-xs font-semibold text-tertiary">Night phase</p>
              <p className="mt-1 font-mono text-[10px] text-ink-dim">Agents asleep</p>
            </div>
          )}

          {!asleep && messages.length === 0 && (
            <p className="py-10 text-center font-mono text-[11px] text-ink-faint">Waiting for discussion…</p>
          )}

          {messages.map((m, i) =>
            m.moderator ? (
              <div key={m.id} className="flex justify-center animate-chat-in" style={{ animationDelay: `${i * 30}ms` }}>
                <div className="w-full rounded-lg border border-border-soft bg-surface-container/80 px-3 py-2 text-center">
                  <span className="label-caps text-[9px] text-tertiary">Moderator</span>
                  <p className="mt-1 text-[11px] leading-relaxed text-ink-dim">{m.text}</p>
                </div>
              </div>
            ) : (
              <ChatRow
                key={m.id}
                message={m}
                active={m.from === activeSpeakerId}
                showRole={showRoles}
                delayMs={i * 30}
              />
            ),
          )}

          {thinkingAgentId != null && !asleep && <TypingRow agentId={thinkingAgentId} />}
        </div>

        {!pinned && messages.length > 0 && (
          <button
            type="button"
            onClick={() => {
              setPinned(true);
              scrollToLatest("smooth");
            }}
            className="absolute bottom-3 left-1/2 z-10 -translate-x-1/2 rounded-full border border-primary/40 bg-bg-deep/95 px-3 py-1.5 font-mono text-[10px] uppercase tracking-caps text-primary shadow-lg backdrop-blur-sm transition hover:border-primary/60 hover:bg-bg-deep"
          >
            ↓ Latest
          </button>
        )}
      </div>
    </div>
  );
}

function ChatRow({
  message,
  active,
  showRole,
  delayMs,
}: {
  message: MafiaChatMessage;
  active?: boolean;
  showRole?: boolean;
  delayMs: number;
}) {
  const profile =
    message.from != null ? profileFor(message.from, message.fromName ?? `Agent ${message.from}`) : null;
  const toneKey = message.tone?.toUpperCase() ?? message.tone ?? "";
  const toneCls = TONE_STYLE[toneKey] ?? TONE_STYLE[message.tone ?? ""] ?? "border-border-soft bg-surface-slate/60";

  return (
    <article
      className={cx(
        "animate-chat-in flex gap-2 rounded-xl border p-2.5 transition-shadow",
        toneCls,
        active && "mafia-chat-active ring-1 ring-primary/40 shadow-glow-teal",
      )}
      style={{ animationDelay: `${delayMs}ms` }}
    >
      {profile && (
        <AgentAvatar profile={profile} size="sm" speaking={active} status={active ? "speaking" : "alive"} />
      )}
      <div className="min-w-0 flex-1">
        <header className="flex flex-wrap items-baseline gap-x-1.5 gap-y-0.5">
          <span className="truncate font-display text-[12px] font-semibold text-ink-primary">
            {message.fromName?.split("_")[0]}
          </span>
          {message.timestamp && (
            <time className="font-mono text-[9px] text-ink-faint">{message.timestamp}</time>
          )}
        </header>
        {message.tone && (
          <span className="mt-0.5 inline-block rounded border border-current/25 px-1.5 py-0.5 font-mono text-[8px] uppercase">
            {message.tone}
            {message.targetName ? ` → ${message.targetName.split("_")[0]}` : ""}
          </span>
        )}
        {showRole && profile && (
          <p className="mt-0.5 font-mono text-[9px] text-ink-faint">{profile.personality}</p>
        )}
        <p className="mt-1.5 text-[12px] leading-relaxed text-ink-primary">{message.text}</p>
      </div>
    </article>
  );
}

function TypingRow({ agentId }: { agentId: number }) {
  const profile = profileFor(agentId, `Agent ${agentId}`);
  return (
    <div className="flex items-center gap-2 rounded-xl border border-border-soft bg-surface-slate/60 px-3 py-2 animate-chat-in">
      <AgentAvatar profile={profile} size="sm" status="thinking" />
      <div className="min-w-0">
        <span className="truncate font-display text-[11px] font-semibold text-ink-dim">{profile.name}</span>
        <div className="mt-0.5 flex items-center gap-1">
          <span className="mafia-thinking-dot h-1 w-1 rounded-full bg-tertiary" />
          <span className="mafia-thinking-dot h-1 w-1 rounded-full bg-tertiary [animation-delay:150ms]" />
          <span className="mafia-thinking-dot h-1 w-1 rounded-full bg-tertiary [animation-delay:300ms]" />
          <span className="ml-1 font-mono text-[9px] text-ink-faint">Thinking…</span>
        </div>
      </div>
    </div>
  );
}
