import Link from "next/link";

import { fetchQuery } from "@/lib/pyyol-lens-api";
import { ktoks, ms } from "@/lib/benchmark-format";

// Per-move detail as emitted by the arena in the benchmark_recorded payload.
type TokenUsage = {
  prompt_tokens?: number;
  completion_tokens?: number;
  reasoning_tokens?: number;
  total_tokens?: number;
};
type DecisionLogEntry = {
  round?: number;
  action?: string;
  outcome?: string;
  latency_ms?: number;
  rationale?: string;
  usage?: TokenUsage;
};
type BenchmarkPayload = {
  seat?: number;
  decisions?: number;
  legal_rate?: number;
  fallback_rate?: number;
  latency_avg_ms?: number;
  prompt_tokens?: number;
  completion_tokens?: number;
  reasoning_tokens?: number;
  total_tokens?: number;
  result?: string;
  decision_log?: DecisionLogEntry[];
};
type MatchDecisions = {
  match_id: string;
  agents: { agent_id: string; game: string; benchmark: BenchmarkPayload }[];
};

// A "higher is better" rate → status class (mirrors benchmark-format thresholds).
function outcomeClass(outcome: string | undefined): string {
  if (outcome === "ok") return "status-ok";
  if (outcome === "illegal_move" || outcome === "timeout") return "status-error";
  return "status-warn";
}

export default async function MatchDecisionsPage({
  params,
}: {
  params: Promise<{ matchId: string }>;
}) {
  const { matchId } = await params;
  const id = decodeURIComponent(matchId);

  const data = await fetchQuery<MatchDecisions>(
    `/v1/matches/${encodeURIComponent(id)}/decisions`,
  );
  const agents = data?.agents ?? [];

  return (
    <div className="page">
      <section className="hero">
        <div>
          <p style={{ margin: 0 }}>
            <Link className="agent-link" href="/benchmarks">
              ← Benchmarks
            </Link>
          </p>
          <h1 className="mono">Match {id}</h1>
          <p>
            Every agent&apos;s decision trail — what they did, how fast, at what token cost,
            and <em>why</em> (agent-reported reasoning).
          </p>
        </div>
      </section>

      {agents.length === 0 && (
        <section className="panel">
          <p className="empty-state">
            No decision data for this match yet. Reasoning &amp; token usage appear once a
            benchmarked match with a hosted agent completes and telemetry is enabled.
          </p>
        </section>
      )}

      {agents.map((a) => {
        const b = a.benchmark ?? {};
        const log = b.decision_log ?? [];
        return (
          <section className="panel" key={a.agent_id}>
            <h2 className="panel-title">
              <Link className="agent-link" href={`/benchmarks/${encodeURIComponent(a.agent_id)}`}>
                {a.agent_id}
              </Link>{" "}
              <span className="muted">
                · {a.game}
                {b.seat != null ? ` · seat ${b.seat}` : ""}
                {b.result ? ` · ${b.result}` : ""}
              </span>
            </h2>

            {/* Token-usage + reliability stat tiles */}
            <section className="cards">
              <article className="card">
                <p className="card-label">Decisions</p>
                <p className="card-value">{(b.decisions ?? 0).toLocaleString()}</p>
              </article>
              <article className="card">
                <p className="card-label">Avg round-trip</p>
                <p className="card-value">{ms(b.latency_avg_ms ?? 0)}</p>
              </article>
              <article className="card">
                <p className="card-label">Total tokens</p>
                <p className="card-value">{b.total_tokens ? ktoks(b.total_tokens) : "—"}</p>
              </article>
              <article className="card">
                <p className="card-label">Reasoning tokens</p>
                <p className="card-value">{b.reasoning_tokens ? ktoks(b.reasoning_tokens) : "—"}</p>
              </article>
            </section>

            {/* Per-move decision trail */}
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Round</th>
                    <th>Action</th>
                    <th>Outcome</th>
                    <th>Latency</th>
                    <th>Tokens</th>
                    <th>Reasoning</th>
                  </tr>
                </thead>
                <tbody>
                  {log.length ? (
                    log.map((d, i) => (
                      <tr key={i}>
                        <td>{d.round ?? "—"}</td>
                        <td className="mono">{d.action || "—"}</td>
                        <td>
                          <span className={outcomeClass(d.outcome)}>{d.outcome || "—"}</span>
                        </td>
                        <td>{ms(d.latency_ms ?? 0)}</td>
                        <td>{d.usage?.total_tokens ? ktoks(d.usage.total_tokens) : <span className="muted">—</span>}</td>
                        <td>{d.rationale ? d.rationale : <span className="muted">—</span>}</td>
                      </tr>
                    ))
                  ) : (
                    <tr>
                      <td colSpan={6} className="empty-state">
                        No per-move trail recorded for this agent.
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </section>
        );
      })}
    </div>
  );
}
