"use client";

import * as React from "react";
import Link from "next/link";
import { cn } from "@/lib/cn";

// Shared matte-black auth/onboarding kit — same theme + smoothness as the
// landing page (#090909, white type, indigo accents, Plus Jakarta).

export const authInput =
  "w-full rounded-lg border border-white/12 bg-white/[0.03] px-3.5 py-2.5 text-sm text-white placeholder:text-white/30 outline-none transition focus:border-indigo-400 focus:ring-2 focus:ring-indigo-500/20";

export function AuthLayout({
  children,
  step,
  maxWidth = "max-w-md",
}: {
  children: React.ReactNode;
  step?: { n: number; total: number; label: string };
  maxWidth?: string;
}) {
  return (
    <div className="flex min-h-screen flex-col bg-[#090909] font-jakarta text-white antialiased">
      <header className="flex items-center justify-between px-5 py-5 sm:px-8">
        <Link href="/" className="flex items-center gap-2">
          <span className="flex h-7 w-7 items-center justify-center rounded-lg bg-indigo-500 text-sm">◆</span>
          <span className="text-sm font-semibold tracking-tight">Pyyol</span>
        </Link>
        <Link href="/" className="text-sm text-white/50 transition-colors hover:text-white">
          ← Back home
        </Link>
      </header>

      <main className="grid flex-1 place-items-center px-5 py-8">
        <div className={cn("w-full", maxWidth)}>
          {step && <StepBar {...step} />}
          {children}
        </div>
      </main>
    </div>
  );
}

export function StepBar({ n, total, label }: { n: number; total: number; label: string }) {
  return (
    <div className="mb-5">
      <div className="mb-2 flex items-center justify-between font-mono text-[10px] uppercase tracking-widest text-white/40">
        <span>{label}</span>
        <span>
          Step {n} / {total}
        </span>
      </div>
      <div className="flex gap-1.5">
        {Array.from({ length: total }).map((_, i) => (
          <span key={i} className={cn("h-1 flex-1 rounded-full transition-colors", i < n ? "bg-indigo-500" : "bg-white/10")} />
        ))}
      </div>
    </div>
  );
}

export function AuthCard({ className, children }: { className?: string; children: React.ReactNode }) {
  return <div className={cn("rounded-2xl border border-white/8 bg-white/[0.02] p-7", className)}>{children}</div>;
}

export function AuthTitle({ title, subtitle }: { title: React.ReactNode; subtitle?: React.ReactNode }) {
  return (
    <div className="mb-6">
      <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
      {subtitle && <p className="mt-2 text-sm text-white/55">{subtitle}</p>}
    </div>
  );
}

export function Field({ label, hint, children }: { label: string; hint?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-1.5 flex items-center justify-between">
        <label className="font-mono text-[10px] uppercase tracking-widest text-white/40">{label}</label>
        {hint}
      </div>
      {children}
    </div>
  );
}

export function PrimaryButton({ className, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      className={cn(
        "flex w-full items-center justify-center gap-2 rounded-lg bg-indigo-500 px-4 py-2.5 text-sm font-medium text-white transition-all hover:bg-indigo-400 disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}

export function GhostButton({ className, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      className={cn(
        "flex items-center justify-center gap-2 rounded-lg border border-white/15 px-4 py-2.5 text-sm font-medium text-white/80 transition-colors hover:bg-white/5 disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}

export function ErrorNote({ children }: { children: React.ReactNode }) {
  return (
    <p className="mt-3 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 font-mono text-[11px] text-red-400">✕ {children}</p>
  );
}

export function Divider({ children }: { children?: React.ReactNode }) {
  return (
    <div className="my-6 flex items-center gap-3">
      <span className="h-px flex-1 bg-white/10" />
      {children && <span className="font-mono text-[10px] uppercase tracking-widest text-white/40">{children}</span>}
      <span className="h-px flex-1 bg-white/10" />
    </div>
  );
}

// StatusScreen — centered confirmation screen for checkout/payout/subscription
// return pages. Matte-black, minimal, one icon + message + CTA.
export function StatusScreen({
  tone = "ok",
  eyebrow,
  title,
  message,
  cta,
  note,
}: {
  tone?: "ok" | "error" | "pending";
  eyebrow?: string;
  title: string;
  message: React.ReactNode;
  cta?: { label: string; href: string };
  note?: React.ReactNode;
}) {
  const mark = { ok: "text-emerald-400", error: "text-red-400", pending: "text-indigo-400" }[tone];
  const glyph = { ok: "✓", error: "✕", pending: "●" }[tone];
  return (
    <AuthLayout maxWidth="max-w-lg">
      <div className="text-center">
        <div className={cn("mx-auto flex h-12 w-12 items-center justify-center rounded-full border text-lg", mark, "border-current/30")}>
          <span className={tone === "pending" ? "live-dot" : ""}>{glyph}</span>
        </div>
        {eyebrow && <p className="mt-5 font-mono text-[10px] uppercase tracking-widest text-white/40">{eyebrow}</p>}
        <h1 className="mt-2 text-2xl font-semibold tracking-tight">{title}</h1>
        <div className="mx-auto mt-4 max-w-md rounded-2xl border border-white/8 bg-white/[0.02] p-6 text-sm text-white/60">{message}</div>
        {note && <p className="mt-3 font-mono text-[11px] text-white/30">{note}</p>}
        {cta && (
          <Link href={cta.href} className="mt-6 inline-flex items-center gap-2 rounded-lg bg-indigo-500 px-6 py-2.5 text-sm font-medium text-white transition-all hover:bg-indigo-400">
            {cta.label}
          </Link>
        )}
      </div>
    </AuthLayout>
  );
}

// CopyField — shows a value with a copy button (used on the "connect your agent"
// credential screens).
export function CopyField({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = React.useState(false);
  return (
    <div>
      <label className="mb-1.5 block font-mono text-[10px] uppercase tracking-widest text-white/40">{label}</label>
      <div className="flex items-center gap-2 rounded-lg border border-white/12 bg-white/[0.03] px-3 py-2">
        <code className="flex-1 truncate font-mono text-[12px] text-white/80">{value}</code>
        <button
          onClick={() => {
            navigator.clipboard?.writeText(value);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          }}
          className="shrink-0 font-mono text-[11px] text-indigo-400 hover:text-indigo-300"
        >
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
    </div>
  );
}
