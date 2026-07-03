import * as React from "react";
import { Brand, Pill } from "./ui";

// Centered, atmospheric shell for the onboarding / auth screens.
export function AuthShell({
  children,
  step,
  badge,
}: {
  children: React.ReactNode;
  step?: { n: number; total: number; label: string };
  badge?: React.ReactNode;
}) {
  return (
    <div className="relative flex min-h-screen flex-col">
      <div className="pointer-events-none absolute inset-0 -z-10 bg-redact opacity-[0.4]" />
      <header className="flex items-center justify-between px-6 py-5">
        <Brand name="AGENT ARENA" />
        {badge ?? (
          <Pill tone="teal" dot>
            ARENA ONLINE
          </Pill>
        )}
      </header>
      <main className="flex flex-1 items-center justify-center px-6 py-10">
        <div className="w-full max-w-md">
          {step && (
            <div className="mb-6">
              <div className="mb-2 flex items-center justify-between">
                <span className="label-caps text-primary">{step.label}</span>
                <span className="font-mono text-[11px] text-ink-dim">
                  STEP {step.n}/{step.total}
                </span>
              </div>
              <div className="flex gap-1.5">
                {Array.from({ length: step.total }, (_, i) => (
                  <div
                    key={i}
                    className={
                      "h-1 flex-1 rounded-full " +
                      (i < step.n ? "bg-primary-container shadow-glow-teal" : "bg-border-strong")
                    }
                  />
                ))}
              </div>
            </div>
          )}
          {children}
        </div>
      </main>
    </div>
  );
}
