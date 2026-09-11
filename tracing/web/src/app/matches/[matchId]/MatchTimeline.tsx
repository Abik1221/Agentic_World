import { ms } from "@/lib/benchmark-format";
import {
  colorForEventType,
  uniqueSpanTypes,
} from "@/lib/span-colors";

// The match, drawn as it happened.
//
// A table of decisions tells you what each move was; it does not tell you the shape of
// the match — who was waiting on whom, where the long stalls were, whether the chat
// clustered before a vote or after it. Those are the questions an operator actually
// opens a match to answer, and they are answered by position on a shared time axis,
// not by reading rows.
//
// So: one lane per agent, every event placed at its real offset from kickoff. Anything
// that reads oddly on the page (a gap, a cluster, one lane dense while another is
// empty) corresponds to something that genuinely happened.

export type TimelineEntry = {
  event_id: string;
  type: string;
  at: string;
  status: string;
  agent_id: string;
  game: string;
  latency_ms: number;
  error?: string;
  detail?: Record<string, unknown>;
};

function detailStr(d: Record<string, unknown> | undefined, k: string): string {
  const v = d?.[k];
  return typeof v === "string" ? v : "";
}

function markColor(e: TimelineEntry): string {
  return colorForEventType(e.type, e.status);
}

const TYPE_LABEL: Record<string, string> = {
  agent_decision: "decision",
  agent_said: "said",
  agent_say_rejected: "silenced",
};

function parseEntryMs(at: string): number {
  const t = Date.parse(at);
  return Number.isFinite(t) ? t : Number.NaN;
}

export function MatchTimeline({ entries }: { entries: TimelineEntry[] }) {
  if (entries.length === 0) return null;

  const times = entries.map((e) => parseEntryMs(e.at)).filter((t) => Number.isFinite(t));
  const start = times.length ? Math.min(...times) : 0;
  const end = times.length ? Math.max(...times) : start + 1;
  // Guard the degenerate case: every event in the same millisecond (a replayed or
  // simulated match) would otherwise divide by zero and collapse the axis.
  const span = Math.max(end - start, 1);

  const lanes = Array.from(new Set(entries.map((e) => e.agent_id))).sort();
  const durationLabel = ms(span);
  const tickCount = 4;
  const ticks = Array.from({ length: tickCount + 1 }, (_, i) => ({
    frac: i / tickCount,
    label: i === 0 ? "0" : ms((span * i) / tickCount),
  }));
  const legendTypes = uniqueSpanTypes(entries.map((e) => e.type));

  return (
    <section className="panel">
      <h2 className="panel-title">
        Match timeline <span className="muted">· {durationLabel} · {entries.length} events</span>
      </h2>
      <p className="muted" style={{ marginTop: 0, fontSize: "0.8rem" }}>
        One lane per agent, positioned by when each event actually occurred. Marks are
        colored by event type (red is an error).
      </p>

      <div className="mt-legend">
        {legendTypes.map((t) => (
          <span key={t} className="mt-legend-item">
            <span className="mt-legend-swatch" style={{ background: colorForEventType(t) }} />
            {t}
          </span>
        ))}
      </div>

      <div className="mt-timeline">
        {lanes.map((agent) => {
          const own = entries.filter((e) => e.agent_id === agent);
          return (
            <div className="mt-lane" key={agent}>
              <div className="mt-lane-label mono" title={agent}>
                {agent}
              </div>
              <div className="mt-lane-track">
                {own.map((e) => {
                  const atMs = parseEntryMs(e.at);
                  const left = Number.isFinite(atMs) ? ((atMs - start) / span) * 100 : 0;
                  const label = TYPE_LABEL[e.type] ?? e.type;
                  const action = detailStr(e.detail, "action");
                  const text = detailStr(e.detail, "text");
                  const rationale = detailStr(e.detail, "rationale");
                  const offsetLabel = Number.isFinite(atMs) ? ms(Math.max(0, atMs - start)) : "";
                  // Everything worth knowing about the mark is in its tooltip, so the
                  // diagram stays readable while nothing is actually hidden.
                  const tip = [
                    `${label}${action ? ` · ${action}` : ""} · ${offsetLabel}`,
                    e.latency_ms ? `latency ${ms(e.latency_ms)}` : "",
                    text ? `“${text}”` : "",
                    rationale,
                    e.error,
                  ]
                    .filter(Boolean)
                    .join("\n");
                  return (
                    <span
                      key={e.event_id}
                      className="mt-mark"
                      style={{ left: `${left}%`, background: markColor(e) }}
                      title={tip}
                    >
                      <span className="mt-mark-time">{offsetLabel}</span>
                    </span>
                  );
                })}
              </div>
            </div>
          );
        })}

        <div className="mt-axis">
          <span className="mt-axis-gutter" />
          <div className="mt-axis-track">
            {ticks.map((t) => (
              <span
                key={t.frac}
                className="mt-axis-tick"
                style={{ left: `${t.frac * 100}%` }}
              >
                {t.label}
              </span>
            ))}
          </div>
        </div>
      </div>
    </section>
  );
}
