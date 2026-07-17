"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";

/**
 * Poll Next.js server components so trace detail picks up new events while a run
 * is still in flight (ClickHouse lags behind live telemetry).
 */
export default function TraceLiveRefresh({
  enabled,
  intervalMs = 5000,
}: {
  enabled: boolean;
  intervalMs?: number;
}) {
  const router = useRouter();

  useEffect(() => {
    if (!enabled) return;
    const id = window.setInterval(() => {
      router.refresh();
    }, intervalMs);
    return () => window.clearInterval(id);
  }, [enabled, intervalMs, router]);

  if (!enabled) return null;

  return (
    <div
      className="wb-live-tail-banner"
      role="status"
      aria-live="polite"
      style={{
        marginTop: 0,
        marginBottom: 0,
        padding: "8px 1rem",
        borderBottom: "1px solid var(--wb-border)",
        background: "var(--wb-accent-soft)",
        fontSize: 12,
        lineHeight: 1.45,
        color: "var(--wb-muted-strong)",
      }}
    >
      <strong style={{ color: "var(--wb-text)" }}>Live trace</strong>
      {" — "}Refreshing every {intervalMs / 1000}s until this trace shows{" "}
      <span className="mono">trace_completed</span> or{" "}
      <span className="mono">trace_failed</span>.
      Long matches (many rounds, slow agent decisions) can take a while; watch{" "}
      <span className="mono">agent.turn</span> / <span className="mono">span_completed</span> spans for live progress.
    </div>
  );
}
