import { QueueCards, type FunnelData, type OwnerRow } from "@/components/queue/QueueCards";
import { fetchArena } from "@/lib/pyyol-lens-api";

/**
 * Queue health: where agents get stuck between joining the queue and playing.
 *
 * The data lives in the ARENA's Postgres, not Lens's ClickHouse, and stays there — copying it
 * would give two stores that can disagree about "how many agents never matched", and a metric
 * with two answers is worse than one fetched over HTTP. See fetchArena.
 */

const WINDOW_SECONDS = 3600;

export default async function QueuePage() {
  const [funnelRes, ownersRes] = await Promise.all([
    fetchArena<{ window_seconds: number; funnel: FunnelData }>(
      `/v1/admin/queue-health?window_seconds=${WINDOW_SECONDS}`,
    ),
    fetchArena<{ owners: OwnerRow[] | null }>(
      `/v1/admin/queue-health/owners?window_seconds=${WINDOW_SECONDS}&limit=50`,
    ),
  ]);

  // Every failure renders as ITSELF, never as an empty funnel. Zeros here would read as "the
  // queue is healthy" — the single wrong conclusion this page must not invite.
  if (!funnelRes.ok) {
    return (
      <main className="page">
        <h1>Queue health</h1>
        <div className="panel">
          <p className={funnelRes.reason === "unauthorized" ? "status-warn" : "status-error"}>
            {funnelRes.reason === "unconfigured"
              ? "PYYOL_API_URL is not set, so this deployment cannot reach the arena API. Nothing is being hidden — the funnel simply has no source here."
              : funnelRes.reason === "unauthorized"
                ? "Your session is not authorised for the arena's admin API. The funnel names individual developers, so it is admin-only by design."
                : "The arena API did not answer. This is a connectivity problem, not an empty queue."}
          </p>
        </div>
      </main>
    );
  }

  const owners = ownersRes.ok ? (ownersRes.data.owners ?? []) : [];

  return (
    <main className="page">
      <h1>Queue health</h1>
      <p className="queue-hint">
        What happens between an agent joining the queue and playing a match. Pick a card to
        trace it.
      </p>
      {/* The per-owner list failing while the funnel succeeded is worth saying out loud: the
          headline numbers are still true, but the "who" behind them is missing, and silently
          showing an empty table would look like "nobody is affected". */}
      {!ownersRes.ok && (
        <p className="status-warn">
          Per-developer detail is unavailable ({ownersRes.reason}); the totals below are still
          accurate.
        </p>
      )}
      <QueueCards
        funnel={funnelRes.data.funnel}
        owners={owners}
        windowSeconds={funnelRes.data.window_seconds || WINDOW_SECONDS}
      />
    </main>
  );
}
