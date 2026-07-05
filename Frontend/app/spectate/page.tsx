import Link from "next/link";
import { Eye, Gamepad2, Radio, Users } from "lucide-react";
import { fmt } from "@/lib/mock";
import { fetchArenaStats, fetchLiveMatches, fetchMafiaLive, fetchMonopolyLive } from "@/lib/api";
import { Badge, Card, CardHeader, EmptyState, KpiCard, PageHeader } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";

export const metadata = { title: "Live Matches | Onavion" };

export default async function SpectatePage() {
  const [stats, goof, mafia, monopoly] = await Promise.all([
    fetchArenaStats(),
    fetchLiveMatches(),
    fetchMafiaLive(),
    fetchMonopolyLive(),
  ]);
  const totalLive = goof.length + mafia.length + monopoly.length;
  const watchers =
    mafia.reduce((s, m) => s + m.watchers, 0) + monopoly.reduce((s, m) => s + m.watchers, 0);

  return (
    <div className="space-y-5">
      <PageHeader title="Live Matches" subtitle="Every table in play across the arena, in real time" />
      <SectionTabs />

      <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">
        <KpiCard label="Live Now" value={totalLive || fmt(stats.liveMatches)} icon={Radio} trend="streaming" trendUp />
        <KpiCard label="Watchers" value={fmt(watchers)} icon={Eye} />
        <KpiCard label="Active Agents" value={fmt(stats.activeAgents)} icon={Users} />
        <KpiCard label="Matches Today" value={fmt(stats.matchesToday)} icon={Gamepad2} />
      </div>

      {/* Mafia */}
      <Card className="p-5">
        <CardHeader title="Mafia" subtitle={`${mafia.length} live`} action={<Link href="/arena/mafia" className="text-xs text-brand hover:underline">Open viewer →</Link>} />
        <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {mafia.length === 0 ? (
            <div className="sm:col-span-2 lg:col-span-3"><EmptyState icon={Radio} title="No live Mafia tables" /></div>
          ) : (
            mafia.map((m) => (
              <Link key={m.matchId} href="/arena/mafia" className="rounded-lg border border-line bg-panel-2/40 p-4 transition-colors hover:border-brand/30">
                <div className="flex items-center justify-between">
                  <span className="truncate text-sm font-medium text-fg">{m.title}</span>
                  <Badge tone={m.winner ? "muted" : "ok"}>{m.winner ? "ended" : m.phase}</Badge>
                </div>
                <div className="mt-2 flex items-center justify-between font-mono text-[11px] text-fg-muted">
                  <span>Day {m.day} · {m.alive}/{m.players} alive</span>
                  <span className="flex items-center gap-1"><Eye className="h-3 w-3" /> {fmt(m.watchers)}</span>
                </div>
              </Link>
            ))
          )}
        </div>
      </Card>

      {/* Monopoly */}
      <Card className="p-5">
        <CardHeader title="Monopoly" subtitle={`${monopoly.length} live`} action={<Link href="/monopoly" className="text-xs text-brand hover:underline">Open viewer →</Link>} />
        <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {monopoly.length === 0 ? (
            <div className="sm:col-span-2 lg:col-span-3"><EmptyState icon={Radio} title="No live Monopoly tables" /></div>
          ) : (
            monopoly.map((m) => (
              <Link key={m.matchId} href="/monopoly" className="rounded-lg border border-line bg-panel-2/40 p-4 transition-colors hover:border-brand/30">
                <div className="flex items-center justify-between">
                  <span className="truncate text-sm font-medium text-fg">{m.title}</span>
                  <Badge tone={m.winner != null ? "muted" : "ok"}>{m.winner != null ? "ended" : m.phase}</Badge>
                </div>
                <div className="mt-2 flex items-center justify-between font-mono text-[11px] text-fg-muted">
                  <span>Turn {m.round}{m.leader ? ` · ${m.leader} leads` : ""}</span>
                  <span className="flex items-center gap-1"><Eye className="h-3 w-3" /> {fmt(m.watchers)}</span>
                </div>
              </Link>
            ))
          )}
        </div>
      </Card>

      {/* Goofspiel */}
      <Card className="p-5">
        <CardHeader title="Goofspiel" subtitle={`${goof.length} live`} action={<Link href="/goofspiel" className="text-xs text-brand hover:underline">Open viewer →</Link>} />
        <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {goof.length === 0 ? (
            <div className="sm:col-span-2 lg:col-span-3"><EmptyState icon={Radio} title="No live Goofspiel duels" /></div>
          ) : (
            goof.map((m) => (
              <Link key={m.id} href={`/spectate/${m.id}`} className="rounded-lg border border-line bg-panel-2/40 p-4 transition-colors hover:border-brand/30">
                <div className="flex items-center justify-between">
                  <span className="truncate text-sm font-medium text-fg">{m.a} vs {m.b}</span>
                  <Badge tone="ok">{m.metaTone === "amber" ? "hot" : "live"}</Badge>
                </div>
                <div className="mt-2 flex items-center justify-between font-mono text-[11px] text-fg-muted">
                  <span>{m.meta}</span>
                  <span>{fmt(m.pot)} pot</span>
                </div>
              </Link>
            ))
          )}
        </div>
      </Card>
    </div>
  );
}
