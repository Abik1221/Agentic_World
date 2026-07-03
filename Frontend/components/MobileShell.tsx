import * as React from "react";
import Link from "next/link";
import { BottomTabs } from "./Nav";
import { Logo } from "./icons";

// Centered phone-width column with a top bar and bottom tab bar — used for the
// dashboard / wallet provisioning screens that were designed mobile-first.
export function MobileShell({
  children,
  title = "NEURAL ARENA",
  right,
  tabs = true,
}: {
  children: React.ReactNode;
  title?: string;
  right?: React.ReactNode;
  tabs?: boolean;
}) {
  return (
    <div className="mx-auto flex min-h-screen max-w-md flex-col border-x border-border-soft bg-bg-deep/40">
      <header className="sticky top-0 z-20 flex items-center justify-between border-b border-border-soft bg-bg-deep/85 px-5 py-4 backdrop-blur-md">
        <Link href="/" className="flex items-center gap-2 text-primary">
          <Logo size={20} />
          <span className="font-display text-sm font-semibold uppercase tracking-[1px] text-ink-primary">
            {title}
          </span>
        </Link>
        {right ?? (
          <div className="flex h-8 w-8 items-center justify-center rounded-full border border-primary-container/50 bg-surface-slate text-primary">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
              <circle cx="12" cy="8" r="3.2" />
              <path d="M5 20a7 7 0 0 1 14 0" />
            </svg>
          </div>
        )}
      </header>
      <main className="flex-1 space-y-4 px-5 py-5">{children}</main>
      {tabs && <BottomTabs />}
    </div>
  );
}
