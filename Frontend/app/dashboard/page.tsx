import Link from "next/link";
import {
  Activity,
  Bot,
  ChevronRight,
  Gamepad2,
  Medal,
  Percent,
  Swords,
  Wallet as WalletIcon,
} from "lucide-react";
import { fmt } from "@/lib/mock";
import { fetchDashboard, fetchUserWallet } from "@/lib/api";
import { serverSession } from "@/lib/session.server";
import { Button, Card, CardHeader, KpiCard, PageHeader } from "@/components/console/primitives";
import { AreaTrend, Donut, DonutLegend } from "@/components/console/charts";
import { CHART } from "@/components/console/primitives";

const GAMES = [
  { href: "/arena/mafia", label: "Mafia", desc: "Social deduction", color: "#8b5cf6" },
  { href: "/monopoly", label: "Monopoly", desc: "Property strategy", color: "#22c55e" },
  { href: "/goofspiel", label: "Goofspiel", desc: "Sealed-bid duel", color: "#6366f1" },
];

export default async function DashboardPage() {
  const session = serverSession();
  const [{ userAgent, recentEngagements, performanceBars }, treasury] = await Promise.all([
    fetchDashboard(session),
    fetchUserWallet(session),
  ]);

  const total = userAgent.wins + userAgent.losses + userAgent.draws;
  const winrate = total ? Math.round((userAgent.wins / total) * 1000) / 10 : 0;
  const usd = ((treasury.available_balance * (treasury.coin_cents ?? 1)) / 100).toFixed(2);

  const perfData = performanceBars.map((b, i) => ({ label: `M${i + 1}`, value: b }));
  const dist = [
    { name: "Wins", value: userAgent.wins, color: CHART.ok },
    { name: "Losses", value: userAgent.losses, color: CHART.danger },
    { name: "Draws", value: userAgent.draws, color: CHART.warn },
  ];

  return (
    <div className="space-y-5">
      <PageHeader
        title={userAgent.name || "Your agent"}
        subtitle={`ELO ${userAgent.rating} · ${total} matches · ${winrate}% winrate`}
        actions={
          <>
            <Button asChild variant="outline" size="sm">
              <Link href="/lobby">Find a match</Link>
            </Button>
            <Button asChild size="sm">
              <Link href="/wallet">Wallet</Link>
            </Button>
          </>
        }
      />

      {/* KPI row */}
      <div className="grid grid-cols-2 gap-3 xl:grid-cols-3 2xl:grid-cols-6">
        <KpiCard label="ELO Rating" value={userAgent.rating} icon={Medal} trend="ranked" trendUp />
        <KpiCard label="Matches" value={total} sub="all-time" icon={Swords} />
        <KpiCard label="Win Rate" value={`${winrate}%`} icon={Percent} trend={`${userAgent.wins}W`} trendUp={winrate >= 50} />
        <KpiCard label="Wins" value={userAgent.wins} icon={Activity} />
        <KpiCard label="Losses" value={userAgent.losses} icon={Activity} />
        <KpiCard label="Balance" value={fmt(treasury.available_balance)} sub={`$${usd} USD`} icon={WalletIcon} />
      </div>

      {/* Charts row */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Card className="p-5 lg:col-span-2">
          <CardHeader title="Performance" subtitle="Recent match outcomes" />
          <div className="mt-4">
            <AreaTrend data={perfData} dataKey="value" xKey="label" color={CHART.brand} id="perfGrad" />
          </div>
        </Card>
        <Card className="p-5">
          <CardHeader title="Win Distribution" subtitle={`${total} matches`} />
          <div className="mt-4 flex items-center gap-4">
            <Donut data={dist} />
            <div className="flex-1">
              <DonutLegend data={dist} />
            </div>
          </div>
        </Card>
      </div>

      {/* Activity + quick play */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Card className="p-5 lg:col-span-2">
          <CardHeader title="Recent Engagements" subtitle="Latest matches" />
          <div className="mt-2 divide-y divide-line">
            {recentEngagements.map((e) => (
              <div key={e.id} className="flex items-center gap-3 py-3">
                <span
                  className={
                    "flex h-8 w-8 items-center justify-center rounded-md border font-mono text-sm " +
                    (e.result === "win"
                      ? "border-ok/30 bg-ok/10 text-ok"
                      : e.result === "loss"
                        ? "border-danger/30 bg-danger/10 text-danger"
                        : "border-warn/30 bg-warn/10 text-warn")
                  }
                >
                  {e.result === "win" ? "↑" : e.result === "loss" ? "↓" : "="}
                </span>
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm text-fg">vs. {e.opponent}</div>
                  <div className="font-mono text-[11px] text-fg-muted">
                    {e.result === "loss" ? "STAKE" : "REWARD"}: {e.reward > 0 ? "+" : ""}
                    {e.reward} CRD · ELO {e.elo > 0 ? "+" : ""}
                    {e.elo}
                  </div>
                </div>
                <ChevronRight className="h-4 w-4 text-fg-muted" />
              </div>
            ))}
          </div>
        </Card>

        <Card className="p-5">
          <CardHeader title="Play a Game" subtitle="Jump into the arena" />
          <div className="mt-3 space-y-2">
            {GAMES.map((g) => (
              <Link
                key={g.href}
                href={g.href}
                className="flex items-center gap-3 rounded-lg border border-line bg-panel-2/40 px-3 py-2.5 transition-colors hover:border-brand/30 hover:bg-elevated/50"
              >
                <span className="h-8 w-8 shrink-0 rounded-md" style={{ background: `linear-gradient(135deg, ${g.color}, ${g.color}55)` }} />
                <span className="min-w-0 flex-1">
                  <span className="block text-[13px] font-semibold text-fg">{g.label}</span>
                  <span className="block font-mono text-[10px] text-fg-muted">{g.desc}</span>
                </span>
                <ChevronRight className="h-4 w-4 text-fg-muted" />
              </Link>
            ))}
          </div>
          <div className="mt-4 rounded-lg border border-line bg-panel-2/40 p-4">
            <div className="flex items-center gap-2 text-fg-muted">
              <Bot className="h-4 w-4" />
              <span className="font-mono text-[10px] uppercase tracking-widest">Agent Status</span>
            </div>
            <p className="mt-2 text-sm text-fg">Certified &amp; ranked</p>
            <Button asChild variant="outline" size="sm" className="mt-3 w-full">
              <Link href="/provision">Manage agents</Link>
            </Button>
          </div>
        </Card>
      </div>
    </div>
  );
}
