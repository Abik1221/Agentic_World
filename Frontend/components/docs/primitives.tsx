"use client";

import * as React from "react";
import { cx } from "../ui";
import type { Endpoint, Scope, CodeSample } from "@/lib/docs";
import { SCOPE_LABEL } from "@/lib/docs";

// ── Scope badge ──────────────────────────────────────────────────────────────
const scopeTone: Record<Scope, string> = {
  public: "text-tertiary border-tertiary/40 bg-tertiary/10",
  agent: "text-primary border-primary-container/40 bg-primary-container/10",
  user: "text-secondary border-secondary/40 bg-secondary/10",
};

export function ScopeBadge({ scope }: { scope: Scope }) {
  return (
    <span className={cx("pill border shrink-0", scopeTone[scope])}>{SCOPE_LABEL[scope]}</span>
  );
}

// ── HTTP method chip ─────────────────────────────────────────────────────────
const methodTone: Record<string, string> = {
  GET: "text-tertiary bg-tertiary/10",
  POST: "text-primary bg-primary-container/10",
  PATCH: "text-secondary bg-secondary/10",
  DELETE: "text-status-error bg-status-error/10",
};

export function EndpointRow({ ep }: { ep: Endpoint }) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-border-soft py-2.5 last:border-0">
      <span
        className={cx(
          "w-16 shrink-0 rounded-sm px-2 py-1 text-center font-mono text-[11px] font-semibold",
          methodTone[ep.method] ?? "text-ink-dim bg-surface-high/40",
        )}
      >
        {ep.method}
      </span>
      <code className="font-mono text-[13px] text-ink-primary">{ep.path}</code>
      <ScopeBadge scope={ep.scope} />
      <span className="w-full pl-[76px] text-[13px] text-ink-dim sm:w-auto sm:flex-1 sm:pl-0">
        {ep.desc}
      </span>
    </div>
  );
}

// ── Copyable code block ──────────────────────────────────────────────────────
export function CodeBlock({ sample }: { sample: CodeSample }) {
  const [copied, setCopied] = React.useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(sample.code);
      setCopied(true);
      setTimeout(() => setCopied(false), 1400);
    } catch {
      /* clipboard blocked — no-op */
    }
  };
  return (
    <div className="overflow-hidden rounded-lg border border-border-strong bg-bg-deep">
      <div className="flex items-center justify-between border-b border-border-soft bg-surface-high/30 px-3 py-1.5">
        <span className="label-caps text-[11px]">{sample.label ?? sample.lang}</span>
        <button
          onClick={copy}
          className="inline-flex items-center gap-1.5 rounded-sm px-2 py-1 font-mono text-[11px] font-medium uppercase tracking-caps text-ink-faint transition hover:text-primary"
        >
          {copied ? (
            <>
              <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                <path d="M20 6L9 17l-5-5" />
              </svg>
              Copied
            </>
          ) : (
            <>
              <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                <rect x="9" y="9" width="11" height="11" rx="2" />
                <path d="M5 15V5a2 2 0 0 1 2-2h10" />
              </svg>
              Copy
            </>
          )}
        </button>
      </div>
      <pre className="console-scroll overflow-x-auto px-4 py-3">
        <code className="font-mono text-[12.5px] leading-6 text-on-surface">{sample.code}</code>
      </pre>
    </div>
  );
}

// ── Callout ──────────────────────────────────────────────────────────────────
export function Callout({
  tone = "info",
  title,
  children,
}: {
  tone?: "info" | "warn";
  title?: string;
  children: React.ReactNode;
}) {
  const t =
    tone === "warn"
      ? "border-secondary/40 bg-secondary/5"
      : "border-primary-container/40 bg-primary-container/5";
  return (
    <div className={cx("rounded-lg border px-4 py-3 text-[13px] leading-6 text-ink-dim", t)}>
      {title && <p className="mb-1 font-semibold text-ink-primary">{title}</p>}
      {children}
    </div>
  );
}
