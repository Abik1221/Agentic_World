import Link from "next/link";

import { RateCell, WLD } from "@/components/benchmark/bench-cells";
import { Unavailable } from "@/components/Unavailable";
import { fetchQueryResult, type BenchmarkStat, type ProviderBenchmark } from "@/lib/pyyol-lens-api";
import { DAY_WINDOWS, GAMES, ktoks, ms, withParam } from "@/lib/benchmark-format";

const MODES = ["ranked", "sandbox", "practice"] as const;

type Search = Promise<Record<string, string | string[] | undefined>>;

function one(v: string | string[] | undefined): string | undefined {
  return Array.isArray(v) ? v[0] : v;
}

export default async function BenchmarksPage({ searchParams }: { searchParams: Search }) {
  const sp = await searchParams;
  const game = one(sp.game);
  const mode = one(sp.mode);
  const days = one(sp.days) ?? "30";

  const current = { game, mode, days };
  const qs = new URLSearchParams();
  if (game) qs.set("game", game);
  if (mode) qs.set("mode", mode);
  qs.set("days", days);
  qs.set("limit", "100");

  const provQs = new URLSearchParams();
  if (game) provQs.set("game", game);
  provQs.set("days", days);

  const [dataRes, provRes] = await Promise.all([
    fetchQueryResult<{ agents: BenchmarkStat[] }>(`/v1/benchmarks/agents?${qs.toString()}`),
    fetchQueryResult<{ providers: ProviderBenchmark[] }>(`/v1/benchmarks/providers?${provQs.toString()}`),
  ]);
  const rows = dataRes.ok ? (dataRes.data.agents ?? []) : [];
  const providers = provRes.ok ? (provRes.data.providers ?? []) : [];
  const queryDown = !dataRes.ok;

  const totalMatches = rows.reduce((n, r) => n + r.matches, 0);
  const bestWin = rows.length ? Math.max(...rows.map((r) => r.win_rate)) : 0;
  const avgFallback = rows.length ? rows.reduce((n, r) => n + r.fallback_rate, 0) / rows.length : 0;

  const chip = (
    key: "game" | "mode" | "days",
    value: string | undefined,
    label: string,
    active: boolean,
  ) => (
    <Link
      key={`${key}-${value ?? "all"}`}
      href={withParam("/benchmarks", current, key, value)}
      className={`filter-chip${active ? " filter-chip-active" : ""}`}
    >
      {label}
    </Link>
  );

  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Agent Benchmarks</h1>
          <p>
            Ranked by a 95% confidence lower bound on win rate (sample-size aware — a few lucky
            matches won&apos;t top the board), then reliability (legal-move rate) and latency.
            Server-authoritative across all games.
          </p>
          <p style={{ marginTop: 8 }}>
            For per-move reasoning, decisions &amp; token spend, open a match&apos;s{" "}
            <Link className="agent-link" href="/traces">
              decision trail in Traces →
            </Link>
          </p>
        </div>
      </section>

      <section className="panel">
        <div className="filter-row">
          <div className="filter-group">
            <span className="filter-label">Game</span>
            {chip("game", undefined, "All", !game)}
            {GAMES.map((g) => chip("game", g, g, game === g))}
          </div>
          <div className="filter-group">
            <span className="filter-label">Mode</span>
            {chip("mode", undefined, "All", !mode)}
            {MODES.map((m) => chip("mode", m, m, mode === m))}
          </div>
          <div className="filter-group">
            <span className="filter-label">Window</span>
            {DAY_WINDOWS.map((d) => chip("days", String(d), `${d}d`, days === String(d)))}
          </div>
        </div>
      </section>

      {queryDown ? <Unavailable title="Benchmark leaderboard unavailable" /> : null}

      {queryDown ? null : (
      <section className="cards">
        <article className="card">
          <p className="card-label">Ranked agents</p>
          <p className="card-value">{rows.length.toLocaleString()}</p>
        </article>
        <article className="card">
          <p className="card-label">Matches (window)</p>
          <p className="card-value">{totalMatches.toLocaleString()}</p>
        </article>
        <article className="card">
          <p className="card-label">Best win rate</p>
          <p className="card-value">{rows.length ? `${(bestWin * 100).toFixed(1)}%` : "—"}</p>
        </article>
        <article className="card">
          <p className="card-label">Avg fallback rate</p>
          <p className="card-value">{rows.length ? `${(avgFallback * 100).toFixed(1)}%` : "—"}</p>
        </article>
      </section>
      )}

      {queryDown ? null : (
      <>
      <section className="panel">
        <p className="table-count">{rows.length.toLocaleString()} agents</p>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>#</th>
                <th>Agent</th>
                <th>Model</th>
                <th>Game</th>
                <th>Matches</th>
                <th>W / L / D</th>
                <th>Win rate</th>
                <th>Legal rate</th>
                <th>Fallback</th>
                <th>Avg latency</th>
                <th>Tokens/match</th>
                <th>Cost/match</th>
              </tr>
            </thead>
            <tbody>
              {rows.length ? (
                rows.map((r, i) => (
                  <tr key={`${r.agent_id}-${r.game}`}>
                    <td className="rank-cell">{i + 1}</td>
                    <td>
                      <Link className="agent-link" href={`/benchmarks/${encodeURIComponent(r.agent_id)}`}>
                        {r.agent_id}
                      </Link>
                    </td>
                    <td>{r.provider ? `${r.provider}/${r.model}` : <span className="muted">—</span>}</td>
                    <td>{r.game}</td>
                    <td>{r.matches.toLocaleString()}</td>
                    <td>
                      <WLD w={r.wins} l={r.losses} d={r.draws} />
                    </td>
                    <td>
                      <RateCell value={r.win_rate} kind="good" />
                      <span className="rate-sub" title="95% confidence lower bound (ranking basis)">
                        conf {(r.win_rate_lb * 100).toFixed(1)}%
                      </span>
                    </td>
                    <td>
                      <RateCell value={r.legal_rate} kind="good" />
                    </td>
                    <td>
                      <RateCell value={r.fallback_rate} kind="bad" />
                    </td>
                    <td>{ms(r.avg_latency_ms)}</td>
                    {/* Tokens come from either per-move model_call events OR the
                        arena benchmark fallback (model_calls=0 but tokens_per_match>0),
                        so gate on the token figure itself, not model_calls. Cost stays
                        gated on model_calls — the arena doesn't price per move. */}
                    <td>{r.tokens_per_match ? ktoks(r.tokens_per_match) : <span className="muted">—</span>}</td>
                    <td>{r.model_calls ? `$${r.cost_per_match.toFixed(4)}` : <span className="muted">—</span>}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={12} className="empty-state">
                    No benchmark data yet. Run matches with telemetry enabled
                    (PYYOL_LENS_ENABLED=true) to populate the leaderboard.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>

      <section className="panel">
        <h2 className="panel-title">Model providers</h2>
        <p className="table-count">
          Server-authoritative from each agent&apos;s manifest — fastest / most reliable model per game.
        </p>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Provider / Model</th>
                <th>Game</th>
                <th>Agents</th>
                <th>Matches</th>
                <th>Win rate</th>
                <th>Legal rate</th>
                <th>Fallback</th>
                <th>Avg latency</th>
              </tr>
            </thead>
            <tbody>
              {providers.length ? (
                providers.map((p) => (
                  <tr key={`${p.provider}-${p.model}-${p.game}`}>
                    <td className="mono">
                      {p.provider}/{p.model}
                    </td>
                    <td>{p.game}</td>
                    <td>{p.agents.toLocaleString()}</td>
                    <td>{p.matches.toLocaleString()}</td>
                    <td>
                      <RateCell value={p.win_rate} kind="good" />
                    </td>
                    <td>
                      <RateCell value={p.legal_rate} kind="good" />
                    </td>
                    <td>
                      <RateCell value={p.fallback_rate} kind="bad" />
                    </td>
                    <td>{ms(p.avg_latency_ms)}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={8} className="empty-state">
                    No provider data yet — agents need a declared model in their manifest.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
      </>
      )}
    </div>
  );
}
