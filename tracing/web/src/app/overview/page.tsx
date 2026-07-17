import Link from "next/link";

import { RateCell } from "@/components/benchmark/bench-cells";
import { fetchQuery, type BenchmarkStat } from "@/lib/pyyol-lens-api";
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
  const [overview, usage, costs, health, top] = await Promise.all([
    fetchQuery<Overview>("/v1/metrics/overview"),
    fetchQuery<UsageRow[]>("/v1/usage/summary"),
    fetchQuery<CostRow[]>("/v1/costs/summary"),
    fetchQuery<PipelineSummary>("/v1/projections/status/summary"),
    fetchQuery<{ agents: BenchmarkStat[] }>("/v1/benchmarks/agents?days=30&limit=5"),
  ]);

  const topUsage = (usage ?? []).slice(0, 6);
  const topCosts = (costs ?? []).slice(0, 6);
  const topAgents = top?.agents ?? [];
  const pipeline = health?.pipeline;
  const pipelineStatus = pipeline?.status ?? "unknown";

  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Overview</h1>
          <p>
            End-to-end visibility across matches, agent benchmarks, token usage, cost, and failure
            patterns for Pyyol AI workflows.
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
                {health ? "ingest → processor → ClickHouse" : "pipeline status unavailable"}
              </span>
            )}
          </div>
          <div className="filter-group">
            <span className="workspace-pill subtle">
              backlog {(pipeline?.estimated_backlog_events ?? 0).toLocaleString()}
            </span>
            <span className="workspace-pill subtle">
              lag {pipeline?.estimated_lag_seconds != null ? `${pipeline.estimated_lag_seconds}s` : "—"}
            </span>
          </div>
        </div>
      </section>

      <section className="cards">
        <div className="card">
          <div className="card-label">Total Traces</div>
          <div className="card-value">{(overview?.traces_total ?? 0).toLocaleString()}</div>
        </div>
        <div className="card">
          <div className="card-label">Total Events</div>
          <div className="card-value">{(overview?.events_total ?? 0).toLocaleString()}</div>
        </div>
        <div className="card">
          <div className="card-label">Errors</div>
          <div className="card-value">{(overview?.errors ?? 0).toLocaleString()}</div>
        </div>
        <div className="card">
          <div className="card-label">P95 Latency</div>
          <div className="card-value">{Math.round(overview?.p95_latency_ms ?? 0)}ms</div>
        </div>
        <div className="card">
          <div className="card-label">Tokens</div>
          <div className="card-value">{(overview?.total_tokens ?? 0).toLocaleString()}</div>
        </div>
        <div className="card">
          <div className="card-label">Estimated Cost</div>
          <div className="card-value">${(overview?.estimated_cost ?? 0).toFixed(2)}</div>
        </div>
      </section>

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
