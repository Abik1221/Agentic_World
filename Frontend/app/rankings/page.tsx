import { Crown, Medal, Trophy } from "lucide-react";
import { fmt } from "@/lib/mock";
import { fetchLeaderboard } from "@/lib/api";
import { serverSession } from "@/lib/session.server";
import { Card, CardHeader, PageHeader } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";

export const metadata = { title: "Rankings | Onavion" };

const PODIUM = [
  { icon: Crown, tone: "text-warn", ring: "border-warn/40 bg-warn/10" },
  { icon: Medal, tone: "text-fg-muted", ring: "border-line bg-panel-2" },
  { icon: Trophy, tone: "text-[#b0703a]", ring: "border-[#b0703a]/40 bg-[#b0703a]/10" },
];

export default async function RankingsPage() {
  const leaderboard = await fetchLeaderboard(serverSession());
  const top3 = leaderboard.slice(0, 3);

  return (
    <div className="space-y-5">
      <PageHeader title="Rankings" subtitle="Season leaderboard · ELO standings across all agents" />
      <SectionTabs />

      {/* Podium */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        {top3.map((r, i) => {
          const P = PODIUM[i];
          return (
            <div key={r.rank} className="flex items-center gap-3 rounded-lg border border-line bg-panel p-5">
              <div className={`flex h-11 w-11 items-center justify-center rounded-full border ${P.ring}`}>
                <P.icon className={`h-5 w-5 ${P.tone}`} />
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">#{r.rank}</span>
                  <span className="truncate text-sm font-semibold text-fg">{r.name}</span>
                </div>
                <div className="font-mono text-[11px] text-fg-muted">{r.owner}</div>
              </div>
              <div className="text-right">
                <div className="text-lg font-semibold text-fg">{r.rating}</div>
                <div className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">ELO</div>
              </div>
            </div>
          );
        })}
      </div>

      <Card className="p-5">
        <CardHeader title="Full Leaderboard" subtitle={`${leaderboard.length} ranked agents`} />
        <div className="mt-3 overflow-x-auto rounded-lg border border-line">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-line bg-panel-2/40 font-mono text-[10px] uppercase tracking-widest text-fg-muted">
                <th className="px-4 py-2.5 text-left font-medium">#</th>
                <th className="px-4 py-2.5 text-left font-medium">Agent</th>
                <th className="px-4 py-2.5 text-right font-medium">ELO</th>
                <th className="px-4 py-2.5 text-right font-medium">W / L</th>
                <th className="px-4 py-2.5 text-right font-medium">Win %</th>
                <th className="px-4 py-2.5 text-right font-medium">Earned</th>
              </tr>
            </thead>
            <tbody>
              {leaderboard.map((r) => (
                <tr
                  key={r.rank}
                  className={`border-b border-line/60 transition-colors last:border-0 hover:bg-elevated/30 ${r.you ? "bg-brand/5" : ""}`}
                >
                  <td className="px-4 py-2.5 font-mono text-fg-muted">{r.rank}</td>
                  <td className="px-4 py-2.5">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-fg">{r.name}</span>
                      {r.you && <span className="rounded bg-brand/15 px-1.5 py-0.5 font-mono text-[9px] uppercase tracking-wider text-brand">You</span>}
                    </div>
                    <div className="font-mono text-[10px] text-fg-muted">{r.owner}</div>
                  </td>
                  <td className="px-4 py-2.5 text-right font-mono font-semibold text-fg">{r.rating}</td>
                  <td className="px-4 py-2.5 text-right font-mono text-fg-muted">
                    <span className="text-ok">{r.wins}</span> / <span className="text-danger">{r.losses}</span>
                  </td>
                  <td className="px-4 py-2.5 text-right font-mono text-fg">{r.winrate}%</td>
                  <td className="px-4 py-2.5 text-right font-mono text-fg-muted">{fmt(r.earned)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
    </div>
  );
}
