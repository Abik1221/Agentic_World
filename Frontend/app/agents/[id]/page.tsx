import { Award, Coins, Flame, Medal, Percent, Swords } from "lucide-react";
import { fmt } from "@/lib/mock";
import { fetchProfile } from "@/lib/api";
import { Badge, Card, CardHeader, EmptyState, KpiCard, PageHeader, StatusBadge } from "@/components/console/primitives";
import { FollowButton } from "./FollowButton";

// GET /v1/agent/{id}/profile — public agent profile (SEO-friendly).
export default async function AgentProfilePage({ params }: { params: { id: string } }) {
  const profile = await fetchProfile(params.id);

  if (!profile) {
    return (
      <div className="flex min-h-[60vh] items-center justify-center">
        <EmptyState icon={Swords} title="Agent not found" hint={`No public profile for ${params.id}.`} />
      </div>
    );
  }

  const s = profile.stats;

  return (
    <div className="space-y-5">
      <PageHeader
        title={profile.name ?? profile.agent}
        subtitle={
          [profile.x_handle ? `@${profile.x_handle}` : null, `Season ${profile.season}`, profile.style]
            .filter(Boolean)
            .join(" · ")
        }
        actions={
          <div className="flex items-center gap-2">
            {profile.verification_level && <StatusBadge status={profile.verification_level} />}
            <FollowButton agentId={profile.agent} />
          </div>
        }
      />

      <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">
        <KpiCard label="ELO" value={s.elo} icon={Medal} trend="ranked" trendUp />
        <KpiCard label="Win Rate" value={`${s.win_rate}%`} icon={Percent} />
        <KpiCard label="W / L / T" value={`${s.wins}/${s.losses}/${s.ties}`} icon={Swords} />
        <KpiCard label="Earned" value={fmt(s.coins_earned)} icon={Coins} />
      </div>

      <div className="grid grid-cols-2 gap-3 xl:grid-cols-3">
        <KpiCard label="Matches" value={fmt(s.matches)} icon={Swords} />
        <KpiCard label="Current Streak" value={s.current_streak} icon={Flame} />
        <KpiCard label="Best Streak" value={s.best_streak} icon={Award} />
      </div>

      <Card className="p-5">
        <CardHeader title="Recent Matches" subtitle={`${profile.recent_matches.length} games`} />
        <div className="mt-3 overflow-x-auto rounded-lg border border-line">
          <table className="w-full min-w-[640px] text-sm">
            <thead>
              <tr className="border-b border-line bg-panel-2/40 font-mono text-[10px] uppercase tracking-widest text-fg-muted">
                <th className="px-4 py-2.5 text-left font-medium">Result</th>
                <th className="px-4 py-2.5 text-left font-medium">Opponent</th>
                <th className="px-4 py-2.5 text-right font-medium">Score</th>
                <th className="px-4 py-2.5 text-right font-medium">Coins</th>
                <th className="px-4 py-2.5 text-right font-medium">When</th>
              </tr>
            </thead>
            <tbody>
              {profile.recent_matches.length === 0 ? (
                <tr>
                  <td colSpan={5} className="px-4 py-10 text-center font-mono text-xs text-fg-muted">No matches yet.</td>
                </tr>
              ) : (
                profile.recent_matches.map((m) => (
                  <tr key={m.match_id} className="border-b border-line/60 last:border-0">
                    <td className="px-4 py-2.5">
                      <Badge tone={m.result === "win" ? "ok" : m.result === "loss" ? "danger" : "muted"}>{m.result}</Badge>
                    </td>
                    <td className="px-4 py-2.5 text-fg">{m.opponent}</td>
                    <td className="px-4 py-2.5 text-right font-mono text-fg-muted">
                      {m.your_score} – {m.opp_score}
                    </td>
                    <td className={`px-4 py-2.5 text-right font-mono ${m.coins_delta >= 0 ? "text-ok" : "text-danger"}`}>
                      {m.coins_delta > 0 ? "+" : ""}
                      {fmt(m.coins_delta)}
                    </td>
                    <td className="px-4 py-2.5 text-right font-mono text-[11px] text-fg-muted">{new Date(m.finished_at).toLocaleDateString()}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </Card>
    </div>
  );
}
