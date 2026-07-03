"use client";

import { useEffect, useRef } from "react";
import { AgentAvatar } from "./AgentAvatar";
import { profileFor } from "@/lib/agentIdentity";
import { SectionLabel, cx } from "@/components/ui";

export interface ChatMessage {
  id: string;
  from?: number;
  fromName?: string;
  text: string;
  tone?: string;
  target?: number;
  targetName?: string;
  moderator?: boolean;
}

/** Live conversation feed — chat-style, PDF spec. */
export function LiveChatFeed({
  messages,
  asleep,
  className,
}: {
  messages: ChatMessage[];
  asleep?: boolean;
  className?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    ref.current?.scrollTo({ top: ref.current.scrollHeight, behavior: "smooth" });
  }, [messages.length]);

  return (
    <div className={cx("flex min-h-[320px] flex-col rounded-xl border border-border-strong bg-bg-deep/40", className)}>
      <div className="border-b border-border-soft px-4 py-3">
        <SectionLabel className="text-tertiary">Live conversation</SectionLabel>
      </div>
      <div ref={ref} className="flex-1 space-y-3 overflow-y-auto p-4" style={{ maxHeight: 420 }}>
        {asleep && (
          <p className="text-center font-mono text-[12px] text-ink-faint">Agents are asleep — night phase</p>
        )}
        {messages.length === 0 && !asleep && (
          <p className="font-mono text-[12px] text-ink-faint">Discussion hasn&apos;t started yet…</p>
        )}
        {messages.map((m) =>
          m.moderator ? (
            <div key={m.id} className="flex justify-center">
              <div className="rounded-lg border border-border-soft bg-surface-slate/60 px-4 py-2 text-center">
                <span className="label-caps text-tertiary">Moderator</span>
                <p className="mt-1 font-mono text-[12px] text-ink-dim">{m.text}</p>
              </div>
            </div>
          ) : (
            <ChatBubble key={m.id} message={m} />
          ),
        )}
      </div>
    </div>
  );
}

function ChatBubble({ message }: { message: ChatMessage }) {
  const profile = message.from != null ? profileFor(message.from, message.fromName ?? `Agent ${message.from}`) : null;
  return (
    <div className="flex gap-3 rounded-xl border border-border-soft bg-surface-slate/50 p-3 transition hover:border-border-strong">
      {profile && <AgentAvatar profile={profile} size="sm" speaking />}
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-baseline gap-2">
          <span className="font-display text-sm font-semibold text-ink-primary">{message.fromName}</span>
          {message.tone && (
            <span className="rounded-full border border-secondary/40 px-2 py-0.5 font-mono text-[9px] uppercase text-secondary">
              {message.tone}
              {message.targetName ? ` → ${message.targetName}` : ""}
            </span>
          )}
        </div>
        <p className="mt-1 text-[15px] leading-relaxed text-ink-primary">&ldquo;{message.text}&rdquo;</p>
      </div>
    </div>
  );
}
