"use client";

import { useState } from "react";

/**
 * The queue funnel, as a choice of traces rather than one wall of numbers.
 *
 * # Why cards first
 *
 * "Where do agents get stuck" is five different investigations wearing one name. An operator
 * arrives already knowing which one they are on — a developer reported a stuck agent, or the
 * matched rate dropped, or nobody is pairing at all — and a page that renders all five at once
 * makes them hunt for the one they came for.
 *
 * So the funnel is a row of clickable cards, each a headline number, and selecting one reveals
 * ONLY that trace: its detail table and its log lines. One card is open at a time on purpose;
 * two open panels is the wall of numbers again with extra clicks.
 *
 * The cards stay visible while a trace is open, so the number that prompted the click is still
 * on screen next to what it expands into. Losing that context is the usual failure of
 * drill-down UIs — you end up reading a table with no memory of why you opened it.
 */

export type FunnelData = {
  enqueued: number;
  ready_asked: number;
  ready_ok: number;
  matched: number;
  dropped: number;
  requeued: number;
  left: number;
  never_matched: number;
  wait_p50_ms: number;
  wait_p95_ms: number;
  longest_waiting_ms: number;
};

export type OwnerRow = {
  owner_public_id: string;
  owner_name: string;
  enqueued: number;
  matched: number;
  dropped: number;
  never_matched: number;
  worst_wait_ms: number;
};

/** Which investigation the operator picked. */
type TraceKey = "never_matched" | "dropped" | "waiting" | "matched" | "flow";

const TRACES: {
  key: TraceKey;
  label: string;
  /** What this card answers, in the operator's words rather than the schema's. */
  hint: string;
  value: (f: FunnelData) => string;
  /** Severity drives the chip colour: a number being non-zero is not always bad. */
  tone: (f: FunnelData) => "ok" | "warn" | "error";
}[] = [
  {
    key: "never_matched",
    label: "Never matched",
    hint: "Queued and never got a game. No other table can answer this — the queue row is deleted when a match finalises.",
    value: (f) => String(f.never_matched),
    tone: (f) => (f.never_matched === 0 ? "ok" : f.never_matched < 5 ? "warn" : "error"),
  },
  {
    key: "dropped",
    label: "Unreachable",
    hint: "Asked to confirm and never answered, so the seat was dropped and requeued. No stake was taken.",
    value: (f) => String(f.dropped),
    tone: (f) => (f.dropped === 0 ? "ok" : f.dropped < 5 ? "warn" : "error"),
  },
  {
    key: "waiting",
    label: "Longest wait now",
    hint: "The oldest entry still sitting in the queue at this moment, from the live table.",
    value: (f) => humanMs(f.longest_waiting_ms),
    tone: (f) =>
      f.longest_waiting_ms < 60_000 ? "ok" : f.longest_waiting_ms < 300_000 ? "warn" : "error",
  },
  {
    key: "matched",
    label: "Matched",
    hint: "Paired into a match. p50 and p95 are the wait of agents that actually got a game.",
    value: (f) => String(f.matched),
    tone: () => "ok",
  },
  {
    key: "flow",
    label: "Full flow",
    hint: "Every stage in order: queued → asked → confirmed → matched, with the exits.",
    value: (f) => `${f.enqueued} in`,
    tone: () => "ok",
  },
];

export function QueueCards({
  funnel,
  owners,
  windowSeconds,
}: {
  funnel: FunnelData;
  owners: OwnerRow[];
  windowSeconds: number;
}) {
  const [open, setOpen] = useState<TraceKey | null>(null);

  return (
    <div>
      <div className="cards" role="group" aria-label="Queue traces">
        {TRACES.map((t) => {
          const selected = open === t.key;
          return (
            <button
              key={t.key}
              type="button"
              className={`card queue-card${selected ? " queue-card-open" : ""}`}
              // aria-pressed, not aria-expanded: these are a toggle group where one choice
              // replaces another, and the panel they control is shared rather than per-card.
              aria-pressed={selected}
              onClick={() => setOpen(selected ? null : t.key)}
              title={t.hint}
            >
              <span className="card-label">{t.label}</span>
              <span className={`card-value status-${t.tone(funnel)}`}>{t.value(funnel)}</span>
            </button>
          );
        })}
      </div>

      {open === null ? (
        <p className="queue-hint">
          Pick a card to trace it. Window: {humanMs(windowSeconds * 1000)}.
        </p>
      ) : (
        <QueueTrace trace={open} funnel={funnel} owners={owners} />
      )}
    </div>
  );
}

/** The one open investigation: a table plus the log lines behind it. */
function QueueTrace({
  trace,
  funnel,
  owners,
}: {
  trace: TraceKey;
  funnel: FunnelData;
  owners: OwnerRow[];
}) {
  if (trace === "flow") {
    return (
      <div className="panel">
        <h3>Flow</h3>
        {/* A visual bar per stage, widths relative to the largest stage rather than to
            `enqueued`. Relative-to-enqueued looks tidy and is misleading: requeues mean a
            later stage can legitimately exceed the number that entered. */}
        <FlowBars funnel={funnel} />
        <LogLines
          lines={[
            `queued        ${funnel.enqueued}`,
            `ready asked   ${funnel.ready_asked}`,
            `ready ok      ${funnel.ready_ok}`,
            `matched       ${funnel.matched}   p50 ${humanMs(funnel.wait_p50_ms)}  p95 ${humanMs(funnel.wait_p95_ms)}`,
            `dropped       ${funnel.dropped}   (no stake taken)`,
            `requeued      ${funnel.requeued}`,
            `left          ${funnel.left}`,
            `never matched ${funnel.never_matched}`,
          ]}
        />
      </div>
    );
  }

  // The three per-owner traces share one table and differ only in what they sort and
  // highlight — the question changes, the shape of the answer does not.
  const sorted = [...owners].sort((a, b) =>
    trace === "dropped"
      ? b.dropped - a.dropped
      : trace === "waiting"
        ? b.worst_wait_ms - a.worst_wait_ms
        : trace === "matched"
          ? b.matched - a.matched
          : b.never_matched - a.never_matched,
  );

  return (
    <div className="panel">
      <h3>{TRACES.find((t) => t.key === trace)?.label} — by developer</h3>
      {sorted.length === 0 ? (
        <p className="queue-hint">No queue activity from any developer in this window.</p>
      ) : (
        <table className="queue-table">
          <thead>
            <tr>
              <th>Developer</th>
              <th>Queued</th>
              <th>Matched</th>
              <th>Unreachable</th>
              <th>Never matched</th>
              <th>Worst wait</th>
            </tr>
          </thead>
          <tbody>
            {sorted.map((o) => (
              <tr key={o.owner_public_id}>
                <td>{o.owner_name || o.owner_public_id}</td>
                <td>{o.enqueued}</td>
                <td>{o.matched}</td>
                <td className={o.dropped > 0 ? "status-warn" : undefined}>{o.dropped}</td>
                <td className={o.never_matched > 0 ? "status-error" : undefined}>
                  {o.never_matched}
                </td>
                <td>{humanMs(o.worst_wait_ms)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <LogLines
        lines={sorted
          .slice(0, 20)
          .map(
            (o) =>
              `${(o.owner_name || o.owner_public_id).padEnd(24).slice(0, 24)} queued=${o.enqueued} matched=${o.matched} unreachable=${o.dropped} never=${o.never_matched} worst=${humanMs(o.worst_wait_ms)}`,
          )}
      />
    </div>
  );
}

/** Stage bars. Width is relative to the LARGEST stage — see the note at the call site. */
function FlowBars({ funnel }: { funnel: FunnelData }) {
  const stages: [string, number][] = [
    ["queued", funnel.enqueued],
    ["asked", funnel.ready_asked],
    ["confirmed", funnel.ready_ok],
    ["matched", funnel.matched],
  ];
  const peak = Math.max(1, ...stages.map(([, n]) => n));
  return (
    <div className="queue-flow">
      {stages.map(([label, n]) => (
        <div className="queue-flow-row" key={label}>
          <span className="queue-flow-label">{label}</span>
          <span className="queue-flow-track">
            <span
              className="queue-flow-fill"
              style={{ width: `${Math.round((n / peak) * 100)}%` }}
              // The number is the text, not the bar. A bar alone cannot be read exactly and
              // cannot be read at all by a screen reader.
              aria-hidden="true"
            />
          </span>
          <span className="queue-flow-value">{n}</span>
        </div>
      ))}
    </div>
  );
}

/**
 * The log view. Monospace, ordered, copy-pasteable.
 *
 * Kept alongside every chart deliberately: a bar shows that something changed and a log line
 * says what. An operator pasting a line into a ticket is the most common thing that actually
 * happens after looking at a dashboard, and a chart cannot be pasted.
 */
function LogLines({ lines }: { lines: string[] }) {
  if (lines.length === 0) return null;
  return (
    <pre className="queue-log" aria-label="Log view">
      {lines.join("\n")}
    </pre>
  );
}

/** Durations a human reads: 900ms, 12s, 4m 20s, 2h 5m. */
function humanMs(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return "—";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}
