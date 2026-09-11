import Link from "next/link";

import { RateCell, WLD } from "@/components/benchmark/bench-cells";
import { Unavailable } from "@/components/Unavailable";
import { fetchQueryResult, type BenchmarkStat } from "@/lib/pyyol-lens-api";
import { ktoks, ms } from "@/lib/benchmark-format";

export default async function AgentBenchmarkPage({
  params,
}: {
  params: Promise<{ agentId: string }>;
}) {
  const { agentId } = await params;
  const id = decodeURIComponent(agentId);

  const [byGameRes, byVersionRes] = await Promise.all([
    fetchQueryResult<{ agent_id: string; games: BenchmarkStat[] }>(
      `/v1/benchmarks/agents/${encodeURIComponent(id)}`,
    ),
    fetchQueryResult<{ agent_id: string; versions: BenchmarkStat[] }>(
      `/v1/benchmarks/agents/${encodeURIComponent(id)}/versions?days=90`,
    ),
  ]);
  const games = byGameRes.ok ? (byGameRes.data.games ?? []) : [];
  const versions = byVersionRes.ok ? (byVersionRes.data.versions ?? []) : [];

  return (
    <div className="page">
      <section className="hero">
        <div>
          <p style={{ margin: 0 }}>
            <Link className="agent-link" href="/benchmarks">
              ← Benchmarks
            </Link>
          </p>
          <h1 className="mono">{id}</h1>
          <p>Per-game reliability and, below, how this agent&apos;s versions compare (last 90 days).</p>
        </div>
      </section>

      {!byGameRes.ok ? <Unavailable title="Agent benchmark unavailable" /> : null}
      <section className="panel">
        <h2 className="panel-title">By game</h2>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Game</th>
                <th>Matches</th>
                <th>W / L / D</th>
                <th>Win rate</th>
                <th>Legal rate</th>
                <th>Fallback</th>
                <th>Timeout</th>
                <th>Round-trip</th>
                <th>Model latency</th>
                <th>Tokens/match</th>
                <th>Cost/match</th>
                <th>Cost/win</th>
              </tr>
            </thead>
            <tbody>
              {games.length ? (
                games.map((r) => (
                  <tr key={r.game}>
                    <td>{r.game}</td>
                    <td>{r.matches.toLocaleString()}</td>
                    <td>
                      <WLD w={r.wins} l={r.losses} d={r.draws} />
                    </td>
                    <td>
                      <RateCell value={r.win_rate} kind="good" />
                    </td>
                    <td>
                      <RateCell value={r.legal_rate} kind="good" />
                    </td>
                    <td>
                      <RateCell value={r.fallback_rate} kind="bad" />
                    </td>
                    <td>
                      <RateCell value={r.timeout_rate} kind="bad" />
                    </td>
                    <td>{ms(r.avg_latency_ms)}</td>
                    <td>{r.model_calls ? ms(r.avg_model_latency_ms) : <span className="muted">—</span>}</td>
                    <td>{r.model_calls ? ktoks(r.tokens_per_match) : <span className="muted">—</span>}</td>
                    <td>{r.model_calls ? `$${r.cost_per_match.toFixed(4)}` : <span className="muted">—</span>}</td>
                    <td>{r.cost_per_win ? `$${r.cost_per_win.toFixed(4)}` : <span className="muted">—</span>}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={12} className="empty-state">
                    No benchmark data for this agent yet.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>

      <section className="panel">
        <h2 className="panel-title">Version comparison</h2>
        <p className="table-count">
          {versions.length.toLocaleString()} version × game rows — compare iterations for a regression.
        </p>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Version</th>
                <th>Game</th>
                <th>Matches</th>
                <th>Win rate</th>
                <th>Legal rate</th>
                <th>Fallback</th>
                <th>Avg latency</th>
              </tr>
            </thead>
            <tbody>
              {versions.length ? (
                versions.map((r) => (
                  <tr key={`${r.agent_version}-${r.game}`}>
                    <td>
                      <span className="version-tag">{r.agent_version || "—"}</span>
                    </td>
                    <td>{r.game}</td>
                    <td>{r.matches.toLocaleString()}</td>
                    <td>
                      <RateCell value={r.win_rate} kind="good" />
                    </td>
                    <td>
                      <RateCell value={r.legal_rate} kind="good" />
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
                    No versioned benchmark data yet (needs an agent_version on the manifest).
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
