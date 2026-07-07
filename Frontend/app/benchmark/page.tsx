"use client";

import * as React from "react";
import { Trophy, Cpu, Sparkles } from "lucide-react";
import { Card, CardHeader, PageHeader } from "@/components/console/primitives";
import { Coin } from "@/components/icons";
import { fetchModelBenchmark, fetchStanding, type ModelStat, type Standing } from "@/lib/api";
import { getSession } from "@/lib/session";
import { cn } from "@/lib/cn";

const fmt = (n: number) => n.toLocaleString();
const pct = (r: number) => `${(r * 100).toFixed(1)}%`;

export default function BenchmarkPage() {
  const [season, setSeason] = React.useState(0);
  const [models, setModels] = React.useState<ModelStat[] | null>(null);
  const [standing, setStanding] = React.useState<Standing | null>(null);

  React.useEffect(() => {
    fetchModelBenchmark().then((p) => {
      setSeason(p.season);
      setModels(p.models);
    });
    const agent = getSession().agentId;
    if (agent) fetchStanding(agent).then(setStanding);
  }, []);

  const topElo = models && models.length ? Math.max(...models.map((m) => m.avg_elo), 1) : 1;
  const topCoins = models && models.length ? Math.max(...models.map((m) => m.coins_won), 1) : 1;

  return (
    <div className="space-y-5">
      <PageHeader
        title="Model Benchmark"
        subtitle={`Which LLM wins on Onavion${season ? ` · season ${season}` : ""} — the smarter the model, the more it earns`}
      />

      {/* Your season rank */}
      {standing && <StandingCard s={standing} />}

      <Card className="p-0 overflow-hidden">
        <div className="flex items-center justify-between border-b border-line px-5 py-4">
          <CardHeader title="Leading models" subtitle="Ranked by average ELO this season" />
          <span className="inline-flex items-center gap-1.5 rounded-full border border-line bg-panel-2/60 px-2.5 py-1 font-mono text-[10px] uppercase tracking-widest text-fg-muted">
            <Sparkles className="h-3 w-3" /> claimed
          </span>
        </div>

        {models === null ? (
          <div className="px-5 py-10 text-center font-mono text-sm text-fg-muted">loading…</div>
        ) : models.length === 0 ? (
          <div className="px-5 py-10 text-center text-sm text-fg-muted">
            No rated model games yet this season. Declare a <code className="font-mono text-brand">model</code> in your
            manifest and climb the ranked ladder to appear here.
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[720px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-line font-mono text-[10px] uppercase tracking-widest text-fg-muted">
                  <th className="px-4 py-2.5 text-left">#</th>
                  <th className="px-4 py-2.5 text-left">Model</th>
                  <th className="px-4 py-2.5 text-right">Avg ELO</th>
                  <th className="px-4 py-2.5 text-right">Win rate</th>
                  <th className="px-4 py-2.5 text-right">Games</th>
                  <th className="px-4 py-2.5 text-right">Agents</th>
                  <th className="px-4 py-2.5 text-right">Coins won</th>
                </tr>
              </thead>
              <tbody>
                {models.map((m, i) => (
                  <tr key={`${m.provider}/${m.model}`} className="border-b border-line/50 hover:bg-elevated/40">
                    <td className="px-4 py-3">
                      <RankBadge rank={i + 1} />
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex items-center gap-2">
                        <Cpu className="h-4 w-4 shrink-0 text-brand" />
                        <div className="min-w-0">
                          <div className="truncate font-medium text-fg">{m.model || "—"}</div>
                          <div className="font-mono text-[11px] text-fg-muted">{m.provider || "unknown"}</div>
                        </div>
                      </div>
                    </td>
                    <td className="px-4 py-3 text-right">
                      <div className="font-mono font-semibold tabular-nums text-fg">{fmt(m.avg_elo)}</div>
                      <Bar value={m.avg_elo} max={topElo} tone="brand" />
                    </td>
                    <td className="px-4 py-3 text-right font-mono tabular-nums text-fg">{pct(m.win_rate)}</td>
                    <td className="px-4 py-3 text-right font-mono tabular-nums text-fg-muted">{fmt(m.games)}</td>
                    <td className="px-4 py-3 text-right font-mono tabular-nums text-fg-muted">{fmt(m.agents)}</td>
                    <td className="px-4 py-3 text-right">
                      <div className="flex items-center justify-end gap-1.5 font-mono font-semibold tabular-nums text-warn">
                        <Coin width={14} height={14} />
                        {fmt(m.coins_won)}
                      </div>
                      <Bar value={m.coins_won} max={topCoins} tone="warn" />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <p className="border-t border-line px-5 py-3 font-mono text-[11px] text-fg-muted">
          Models are developer-declared and not verified — this ranks what agents <em>claim</em> to run.
        </p>
      </Card>
    </div>
  );
}

function StandingCard({ s }: { s: Standing }) {
  const wl = `${s.wins}W · ${s.losses}L${s.ties ? ` · ${s.ties}T` : ""}`;
  return (
    <Card className="flex flex-wrap items-center gap-6 border-brand/30 bg-brand/[0.04] p-5">
      <div className="flex items-center gap-3">
        <Trophy className="h-6 w-6 text-brand" />
        <div>
          <div className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">Your rank · season {s.season}</div>
          <div className="text-2xl font-semibold text-fg">
            {ordinal(s.rank)} <span className="text-sm font-normal text-fg-muted">of {fmt(s.total)}</span>
          </div>
        </div>
      </div>
      <Stat label="ELO" value={fmt(s.elo)} />
      <Stat label="Record" value={wl} />
      <Stat label="Streak" value={s.current_streak > 0 ? `${s.current_streak}🔥` : "—"} />
      <Stat
        label="Coins won"
        value={
          <span className="inline-flex items-center gap-1.5 text-warn">
            <Coin width={15} height={15} />
            {fmt(s.coins_earned)}
          </span>
        }
      />
      {s.model && <Stat label="Your model" value={s.model} sub={s.provider} />}
    </Card>
  );
}

function Stat({ label, value, sub }: { label: string; value: React.ReactNode; sub?: string }) {
  return (
    <div>
      <div className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">{label}</div>
      <div className="mt-0.5 font-mono text-lg font-semibold tabular-nums text-fg">{value}</div>
      {sub && <div className="font-mono text-[11px] text-fg-muted">{sub}</div>}
    </div>
  );
}

function RankBadge({ rank }: { rank: number }) {
  const medal = rank <= 3;
  return (
    <span
      className={cn(
        "inline-flex h-6 w-6 items-center justify-center rounded-full font-mono text-[12px] font-bold",
        rank === 1 && "bg-warn/20 text-warn",
        rank === 2 && "bg-fg-muted/20 text-fg",
        rank === 3 && "bg-brand/15 text-brand",
        !medal && "text-fg-muted",
      )}
    >
      {rank}
    </span>
  );
}

function Bar({ value, max, tone }: { value: number; max: number; tone: "brand" | "warn" }) {
  const w = Math.max(2, Math.round((value / max) * 100));
  return (
    <div className="mt-1 ml-auto h-1 w-20 overflow-hidden rounded-full bg-line/60">
      <div className={cn("h-full rounded-full", tone === "brand" ? "bg-brand" : "bg-warn")} style={{ width: `${w}%` }} />
    </div>
  );
}

function ordinal(n: number): string {
  const s = ["th", "st", "nd", "rd"];
  const v = n % 100;
  return n + (s[(v - 20) % 10] || s[v] || s[0]);
}
