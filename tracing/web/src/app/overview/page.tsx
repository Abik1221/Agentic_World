import Link from "next/link";

import { RateCell } from "@/components/benchmark/bench-cells";
import { Unavailable } from "@/components/Unavailable";
import { fetchQueryResult, type BenchmarkStat } from "@/lib/pyyol-lens-api";
import { ms } from "@/lib/benchmark-format";

type Overview = {
  traces_total: number;
  events_total: number;
  errors: number;
  p95_latency_ms: number;
  total_tokens: number;
  estimated_cost: number;
};

type UsageRow = {
  project_id: string;
  environment: string;
  provider: string;
  model: string;
  total_tokens: number;
};

type CostRow = {
  project_id: string;
  environment: string;
  provider: string;
  model: string;
  estimated_cost: number;
  currency: string;
};

type PipelineSummary = {
  pipeline?: {
    status?: string;
    status_reason?: string;
    estimated_backlog_events?: number;
    estimated_lag_seconds?: number | null;
  };
};

function healthClass(status: string): string {
  switch (status) {
    case "ok":
      return "status-ok";
    case "degraded":
      return "status-warn";
    default:
      return "status-error"; // stalled / unknown
  }
}

export default async function OverviewPage() {
  const [overviewRes, usageRes, costsRes, healthRes, topRes] = await Promise.all([
    fetchQueryResult<Overview>("/v1/metrics/overview"),
    fetchQueryResult<UsageRow[]>("/v1/usage/summary"),
    fetchQueryResult<CostRow[]>("/v1/costs/summary"),
    fetchQueryResult<PipelineSummary>("/v1/projections/status/summary"),
    fetchQueryResult<{ agents: BenchmarkStat[] }>("/v1/benchmarks/agents?days=30&limit=5"),
  ]);

  const overview = overviewRes.ok ? overviewRes.data : null;
  const topUsage = usageRes.ok ? usageRes.data.slice(0, 6) : [];
  const topCosts = costsRes.ok ? costsRes.data.slice(0, 6) : [];
  const topAgents = topRes.ok ? (topRes.data.agents ?? []) : [];
  const pipeline = healthRes.ok ? healthRes.data.pipeline : undefined;
  const pipelineStatus = pipeline?.status ?? (healthRes.ok ? "unknown" : "unreachable");

  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Overview</h1>
          <p>
            Errors, latency, and spend. Open <Link href="/games">Games</Link> for a match.
          </p>
        </div>
      </section>

      <section className="panel">
        <div className="filter-row" style={{ justifyContent: "space-between" }}>
          <div className="filter-group">
            <span className="filter-label">Pipeline</span>
            <span className={`status ${healthClass(pipelineStatus)}`}>{pipelineStatus}</span>
            {pipeline?.status_reason ? (
              <span style={{ color: "var(--muted)", fontSize: "0.8rem" }}>{pipeline.status_reason}</span>
            ) : (
              <span style={{ color: "var(--muted)", fontSize: "0.8rem" }}>
                {healthRes.ok ? "ingest → processor → ClickHouse" : "pipeline status unavailable"}
              </span>
            )}
          </div>
          <div className="filter-group">
            <span className="workspace-pill subtle">
              backlog {healthRes.ok ? (pipeline?.estimated_backlog_events ?? 0).toLocaleString() : "—"}
            </span>
            <span className="workspace-pill subtle">
              lag {healthRes.ok && pipeline?.estimated_lag_seconds != null ? `${pipeline.estimated_lag_seconds}s` : "—"}
            </span>
          </div>
        </div>
      </section>

      {overview ? (
        <section className="cards">
          <div className="card">
            <div className="card-label">Errors</div>
            <div className="card-value">{overview.errors.toLocaleString()}</div>
          </div>
          <div className="card">
            <div className="card-label">P95</div>
            <div className="card-value">{Math.round(overview.p95_latency_ms)}ms</div>
          </div>
          <div className="card">
            <div className="card-label">Cost</div>
            <div className="card-value">${overview.estimated_cost.toFixed(2)}</div>
          </div>
        </section>
      ) : (
        <Unavailable title="Overview metrics unavailable" />
      )}
      {overview && overview.traces_total === 0 ? (
        <p className="empty-state">No telemetry ingested yet. Zeros above are real, not placeholders.</p>
      ) : null}

      <section className="panel">
        <div className="filter-row" style={{ justifyContent: "space-between" }}>
          <h2 style={{ margin: 0 }}>Top Agents</h2>
          <Link className="agent-link" href="/benchmarks">
            Full leaderboard →
          </Link>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>#</th>
                <th>Agent</th>
                <th>Game</th>
                <th>Matches</th>
                <th>Win rate</th>
                <th>Fallback</th>
                <th>Avg latency</th>
              </tr>
            </thead>
            <tbody>
              {topAgents.length ? (
                topAgents.map((r, i) => (
                  <tr key={`${r.agent_id}-${r.game}`}>
                    <td className="rank-cell">{i + 1}</td>
                    <td>
                      <Link className="agent-link" href={`/benchmarks/${encodeURIComponent(r.agent_id)}`}>
                        {r.agent_id}
                      </Link>
                    </td>
                    <td>{r.game}</td>
                    <td>{r.matches.toLocaleString()}</td>
                    <td>
                      <RateCell value={r.win_rate} kind="good" />
                    </td>
                    <td>
                      <RateCell value={r.fallback_rate} kind="bad" />
                    </td>
                    <td>{ms(r.avg_latency_ms)}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={7} className="empty-state">
                    No benchmark data yet — run matches with telemetry enabled.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>

      <section className="grid-two">
        <div className="panel">
          <h2>Top Token Consumers</h2>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Project</th>
                  <th>Environment</th>
                  <th>Model</th>
                  <th>Tokens</th>
                </tr>
              </thead>
              <tbody>
                {topUsage.length ? (
                  topUsage.map((row, index) => (
                    <tr key={`${row.project_id}-${row.model}-${index}`}>
                      <td>{row.project_id}</td>
                      <td>{row.environment}</td>
                      <td>
                        {row.provider}/{row.model}
                      </td>
                      <td>{row.total_tokens.toLocaleString()}</td>
                    </tr>
                  ))
                ) : (
                  <tr>
                    <td colSpan={4} className="empty-state">
                      No usage recorded yet.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
        <div className="panel">
          <h2>Top Cost Drivers</h2>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Project</th>
                  <th>Environment</th>
                  <th>Model</th>
                  <th>Cost</th>
                </tr>
              </thead>
              <tbody>
                {topCosts.length ? (
                  topCosts.map((row, index) => (
                    <tr key={`${row.project_id}-${row.model}-${index}`}>
                      <td>{row.project_id}</td>
                      <td>{row.environment}</td>
                      <td>
                        {row.provider}/{row.model}
                      </td>
                      <td>
                        {row.currency || "USD"} {row.estimated_cost.toFixed(4)}
                      </td>
                    </tr>
                  ))
                ) : (
                  <tr>
                    <td colSpan={4} className="empty-state">
                      No cost data recorded yet.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
      </section>
    </div>
  );
}
