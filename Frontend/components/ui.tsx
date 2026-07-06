import * as React from "react";
import Link from "next/link";
import { Logo } from "./icons";

function cx(...c: (string | false | undefined)[]) {
  return c.filter(Boolean).join(" ");
}

// ---------------------------------------------------------------- Brand mark
export function Brand({
  name = "AGENT ARENA",
  href = "/",
  className,
}: {
  name?: string;
  href?: string;
  className?: string;
}) {
  return (
    <Link href={href} className={cx("group inline-flex items-center gap-2", className)}>
      <span className="text-primary transition group-hover:drop-shadow-[0_0_6px_rgba(140,255,230,0.6)]">
        <Logo />
      </span>
      <span className="font-display text-[15px] font-semibold uppercase tracking-[1px] text-ink-primary">
        {name}
      </span>
    </Link>
  );
}

// ---------------------------------------------------------------- Section label
export function SectionLabel({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return <p className={cx("label-caps", className)}>{children}</p>;
}

// ---------------------------------------------------------------- Panels
export function Panel({
  children,
  className,
  glass,
}: {
  children: React.ReactNode;
  className?: string;
  glass?: boolean;
}) {
  return <div className={cx(glass ? "glass" : "panel", className)}>{children}</div>;
}

// ---------------------------------------------------------------- Status pill
const toneMap = {
  teal: "text-primary border-primary-container/40 bg-primary-container/10",
  amber: "text-secondary border-secondary/40 bg-secondary/10",
  blue: "text-tertiary border-tertiary/40 bg-tertiary/10",
  red: "text-status-error border-status-error/40 bg-status-error/10",
  neutral: "text-ink-dim border-border-strong bg-surface-high/40",
} as const;

export type Tone = keyof typeof toneMap;

export function Pill({
  children,
  tone = "neutral",
  dot,
  className,
}: {
  children: React.ReactNode;
  tone?: Tone;
  dot?: boolean;
  className?: string;
}) {
  return (
    <span className={cx("pill border", toneMap[tone], className)}>
      {dot && (
        <span className={cx("live-dot inline-block h-1.5 w-1.5 rounded-full", dotMap[tone])} />
      )}
      {children}
    </span>
  );
}

const dotMap: Record<Tone, string> = {
  teal: "bg-primary",
  amber: "bg-secondary",
  blue: "bg-tertiary",
  red: "bg-status-error",
  neutral: "bg-ink-faint",
};

// ---------------------------------------------------------------- Buttons
type BtnVariant = "primary" | "ghost" | "amber" | "neutral";
const btnClass: Record<BtnVariant, string> = {
  primary: "btn-primary",
  ghost: "btn-ghost",
  amber: "btn-amber",
  neutral: "btn-neutral",
};

export function Button({
  children,
  variant = "primary",
  href,
  className,
  type,
  full,
  onClick,
  disabled,
}: {
  children: React.ReactNode;
  variant?: BtnVariant;
  href?: string;
  className?: string;
  type?: "button" | "submit";
  full?: boolean;
  onClick?: () => void;
  disabled?: boolean;
}) {
  const cls = cx(btnClass[variant], full && "w-full", disabled && "pointer-events-none opacity-50", className);
  if (href) {
    return (
      <Link href={href} className={cls} onClick={onClick}>
        {children}
      </Link>
    );
  }
  return (
    <button type={type ?? "button"} className={cls} onClick={onClick} disabled={disabled}>
      {children}
    </button>
  );
}

// ---------------------------------------------------------------- Game card
export function GameCard({
  value,
  label,
  active,
  redacted,
  suit,
  size = "md",
  className,
}: {
  value?: number | string;
  label?: string;
  active?: boolean;
  redacted?: boolean;
  suit?: string;
  size?: "sm" | "md" | "lg";
  className?: string;
}) {
  const dims = {
    sm: "h-20 w-full text-2xl",
    md: "h-28 w-full text-3xl",
    lg: "aspect-[3/4.2] w-full text-6xl",
  }[size];

  if (redacted) {
    return (
      <div
        className={cx(
          "flex flex-col items-center justify-center rounded-lg border border-border-strong bg-bg-deep bg-redact",
          dims,
          className,
        )}
      >
        <span className="text-ink-faint opacity-50">
          <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5">
            <rect x="5" y="11" width="14" height="9" rx="1.5" />
            <path d="M8 11V8a4 4 0 0 1 8 0v3" />
          </svg>
        </span>
        {label && <span className="mt-2 label-caps">{label}</span>}
      </div>
    );
  }

  return (
    <div
      className={cx(
        "group relative flex flex-col items-center justify-center rounded-lg border bg-surface-slate/70 bg-panel-grad transition",
        active
          ? "border-primary-container/70 shadow-glow-teal-lg"
          : "border-border-strong hover:border-outline-variant",
        dims,
        className,
      )}
    >
      {suit && size === "lg" && (
        <span className="absolute left-3 top-3 font-display text-xl text-secondary/80">
          {suit}
        </span>
      )}
      {suit && size === "lg" && (
        <span className="absolute bottom-3 right-3 rotate-180 font-display text-xl text-secondary/80">
          {suit}
        </span>
      )}
      {label && size === "lg" && (
        <span className="label-caps mb-1 text-secondary/60">VALUE</span>
      )}
      <span
        className={cx(
          "font-mono font-medium tabular-nums",
          active ? "text-primary" : suit ? "text-secondary" : "text-on-surface",
        )}
      >
        {value}
      </span>
      {label && size !== "lg" && (
        <span className="mt-2 label-caps text-[10px]">{label}</span>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- Stat block
export function Stat({
  label,
  value,
  tone,
  sub,
  className,
}: {
  label: string;
  value: React.ReactNode;
  tone?: "teal" | "amber" | "blue" | "default";
  sub?: string;
  className?: string;
}) {
  const valTone =
    tone === "teal"
      ? "text-primary"
      : tone === "amber"
        ? "text-secondary"
        : tone === "blue"
          ? "text-tertiary"
          : "text-ink-primary";
  return (
    <div className={className}>
      <div className={cx("font-mono text-2xl font-semibold tabular-nums", valTone)}>
        {value}
      </div>
      <div className="mt-1 label-caps">{label}</div>
      {sub && <div className="mt-0.5 font-mono text-[11px] text-ink-faint">{sub}</div>}
    </div>
  );
}

// ---------------------------------------------------------------- Meter bar
export function Meter({
  value,
  tone = "teal",
  label,
  right,
}: {
  value: number;
  tone?: "teal" | "amber";
  label?: string;
  right?: string;
}) {
  return (
    <div>
      {(label || right) && (
        <div className="mb-1.5 flex items-center justify-between">
          {label && <span className="label-caps">{label}</span>}
          {right && (
            <span className="font-mono text-[11px] text-ink-dim">{right}</span>
          )}
        </div>
      )}
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-bg-deep">
        <div
          className={cx(
            "h-full rounded-full",
            tone === "teal"
              ? "bg-primary-container shadow-glow-teal"
              : "bg-secondary shadow-glow-amber",
          )}
          style={{ width: `${Math.min(100, Math.max(0, value))}%` }}
        />
      </div>
    </div>
  );
}

export { cx };
