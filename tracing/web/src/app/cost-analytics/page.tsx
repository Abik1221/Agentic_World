import { Unavailable } from "@/components/Unavailable";
import { fetchQueryResult } from "@/lib/pyyol-lens-api";
import PricingModeller from "./PricingModeller";

export const dynamic = "force-dynamic";

type RunStats = {
  runs: number;
  total_usd: number;
  avg_usd: number;
  median_usd: number;
  p90_usd: number;
  p95_usd: number;
  min_usd: number;
  max_usd: number;
  range_usd: number;
  avg_tokens: number;
  median_tokens: number;
  total_tokens: number;
  prompt_tokens: number;
  completion_tokens: number;
};

type ModelRow = {
  provider: string;
  model: string;
  calls: number;
  runs: number;
  total_usd: number;
  avg_usd_per_call: number;
  avg_usd_per_run: number;
  avg_tokens_per_run: number;
  prompt_tokens: number;
  completion_tokens: number;
  cached_tokens: number;
  reasoning_tokens: number;
  total_tokens: number;
  usd_per_1m_tokens: number;
};

type GroupRow = {
  name: string;
  calls: number;
  runs: number;
  total_usd: number;
  avg_usd_per_run: number;
  total_tokens: number;
  avg_tokens_per_run: number;
};

type DailyRow = {
  date: string;
  total_usd: number;
  total_tokens: number;
  runs: number;
};

type CostAnalytics = {
  from: string;
  to: string;
  run_stats: RunStats;
  by_model: ModelRow[];
  by_operation: GroupRow[];
  daily: DailyRow[];
};

const usd = (n: number, dp = 4) =>
  `$${(n ?? 0).toLocaleString("en-US", {
    minimumFractionDigits: dp,
    maximumFractionDigits: dp,
  })}`;

const tok = (n: number) => {
  const v = n ?? 0;
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(2)}M`;
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}K`;
  return `${Math.round(v)}`;
};

const pct = (n: number) => `${((n ?? 0) * 100).toFixed(1)}%`;

function Stat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div
      style={{
        border: "1px solid var(--border, #232323)",
        borderRadius: 10,
        padding: "14px 16px",
        background: "var(--panel, #141414)",
      }}
    >
      <div style={{ fontSize: 11, textTransform: "uppercase", letterSpacing: 0.5, color: "#8a8a8a" }}>
        {label}
      </div>
      <div style={{ marginTop: 4, fontSize: 20, fontWeight: 600 }}>{value}</div>
      {sub && <div style={{ marginTop: 2, fontSize: 12, color: "#9a9a9a" }}>{sub}</div>}
    </div>
  );
}

export default async function CostAnalyticsPage() {
  // events_raw carries a 30-day TTL, so the default window already covers all
  // retained data. (The backend defaults to the last 30 days when from/to omitted.)
  const result = await fetchQueryResult<CostAnalytics>("/v1/cost-analytics");
  const data = result.ok ? result.data : null;

  const rs = data?.run_stats;
  const blendedUsdPer1M =
    rs && rs.total_tokens ? (rs.total_usd / rs.total_tokens) * 1_000_000 : 0;

  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Cost Analytics</h1>
          <p>
            Deep dive into model spend over the last 30 days — per run, per model, and
            per operation. All costs USD; use the pricing panel to model ETB margins.
          </p>
        </div>
      </section>

      {!result.ok && (
        <section className="panel">
          <Unavailable title="Cost analytics unavailable" />
        </section>
      )}
      {result.ok && !rs && (
        <section className="panel">
          <p className="empty-state">No cost data in the retained window yet.</p>
        </section>
      )}

      {rs && data && (
        <>
          {/* Headline per-run stats */}
          <section className="panel">
            <div
              style={{
                display: "grid",
                gap: 12,
                gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))",
              }}
            >
              <Stat label="Runs" value={rs.runs.toLocaleString()} />
              <Stat label="Total spend" value={usd(rs.total_usd, 2)} />
              <Stat label="Avg / run" value={usd(rs.avg_usd)} sub={`median ${usd(rs.median_usd)}`} />
              <Stat label="p90 / run" value={usd(rs.p90_usd)} sub={`p95 ${usd(rs.p95_usd)}`} />
              <Stat label="Range / run" value={usd(rs.min_usd)} sub={`→ ${usd(rs.max_usd)}`} />
              <Stat
                label="Avg tokens / run"
                value={tok(rs.avg_tokens)}
                sub={`median ${tok(rs.median_tokens)}`}
              />
            </div>
          </section>

          {/* Pricing / margin modeller (client) */}
          <PricingModeller
            blendedUsdPer1M={blendedUsdPer1M}
            avgTokensPerRun={rs.avg_tokens}
          />

          {/* By model */}
          <section className="panel">
            <h2 style={{ marginTop: 0 }}>By model</h2>
            <p className="table-count">{data.by_model.length} models</p>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Provider</th>
                    <th>Model</th>
                    <th>Calls</th>
                    <th>Runs</th>
                    <th>Total USD</th>
                    <th>% cost</th>
                    <th>$/run</th>
                    <th>$/1M tok</th>
                    <th>Tokens</th>
                  </tr>
                </thead>
                <tbody>
                  {data.by_model.length ? (
                    data.by_model.map((m, i) => (
                      <tr key={`${m.provider}-${m.model}-${i}`}>
                        <td>{m.provider || "—"}</td>
                        <td>{m.model}</td>
                        <td>{m.calls.toLocaleString()}</td>
                        <td>{m.runs.toLocaleString()}</td>
                        <td>{usd(m.total_usd, 2)}</td>
                        <td>{pct(rs.total_usd ? m.total_usd / rs.total_usd : 0)}</td>
                        <td>{usd(m.avg_usd_per_run)}</td>
                        <td>{usd(m.usd_per_1m_tokens, 2)}</td>
                        <td>{tok(m.total_tokens)}</td>
                      </tr>
                    ))
                  ) : (
                    <tr>
                      <td colSpan={9} className="empty-state">
                        No model data.
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </section>

          {/* By operation */}
          <GroupTable title="By operation (app)" rows={data.by_operation} total={rs.total_usd} />

          {/* Daily trend */}
          <section className="panel">
            <h2 style={{ marginTop: 0 }}>Daily spend (USD)</h2>
            <DailyBars rows={data.daily} />
          </section>
        </>
      )}
    </div>
  );
}

function GroupTable({
  title,
  rows,
  total,
}: {
  title: string;
  rows: GroupRow[];
  total: number;
}) {
  return (
    <section className="panel">
      <h2 style={{ marginTop: 0 }}>{title}</h2>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Runs</th>
              <th>Total USD</th>
              <th>% cost</th>
              <th>$/run</th>
              <th>Tok/run</th>
            </tr>
          </thead>
          <tbody>
            {rows.length ? (
              rows.map((r, i) => (
                <tr key={`${r.name}-${i}`}>
                  <td>{r.name}</td>
                  <td>{r.runs.toLocaleString()}</td>
                  <td>{usd(r.total_usd, 2)}</td>
                  <td>{pct(total ? r.total_usd / total : 0)}</td>
                  <td>{usd(r.avg_usd_per_run)}</td>
                  <td>{tok(r.avg_tokens_per_run)}</td>
                </tr>
              ))
            ) : (
              <tr>
                <td colSpan={6} className="empty-state">
                  No data.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function DailyBars({ rows }: { rows: DailyRow[] }) {
  const max = Math.max(1, ...rows.map((d) => d.total_usd));
  return (
    <div style={{ display: "flex", alignItems: "flex-end", gap: 2, height: 160, paddingTop: 8 }}>
      {rows.length === 0 && <span className="empty-state">No data.</span>}
      {rows.map((d) => (
        <div
          key={d.date}
          title={`${d.date}: ${usd(d.total_usd, 2)} · ${d.runs} runs`}
          style={{
            flex: 1,
            minWidth: 2,
            height: `${(d.total_usd / max) * 140}px`,
            background: "#6366f1",
            borderRadius: "3px 3px 0 0",
          }}
        />
      ))}
    </div>
  );
}
