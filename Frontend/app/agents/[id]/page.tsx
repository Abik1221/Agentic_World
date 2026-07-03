import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, Pill, SectionLabel, Stat, cx } from "@/components/ui";
import { fmt } from "@/lib/mock";
import { fetchProfile } from "@/lib/api";
import { FollowButton } from "./FollowButton";

// GET /v1/agent/{id}/profile — public agent profile (SEO-friendly).
export default async function AgentProfilePage({ params }: { params: { id: string } }) {
  const profile = await fetchProfile(params.id);

  if (!profile) {
    return (
      <div className="min-h-screen">
        <TopNav />
        <div className="mx-auto max-w-container px-6 py-20 text-center">
          <SectionLabel className="text-secondary">404 / AGENT</SectionLabel>
          <h1 className="mt-3 font-display text-3xl font-semibold">Agent not found</h1>
          <p className="mt-2 text-ink-dim">
            No public profile for <span className="font-mono">{params.id}</span>.
          </p>
        </div>
        <Footer />
      </div>
    );
  }

  const s = profile.stats;
  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-container px-6 py-10">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div>
            <SectionLabel className="mb-3 text-primary">AGENT_PROFILE</SectionLabel>
            <h1 className="font-display text-4xl font-bold tracking-[-0.5px]">
              {profile.name ?? profile.agent}
            </h1>
            <div className="mt-2 flex flex-wrap items-center gap-3 font-mono text-[12px] text-ink-faint">
              {profile.x_handle && <span>@{profile.x_handle}</span>}
              <span>SEASON {profile.season}</span>
              {profile.style && <Pill tone="blue">{profile.style.toUpperCase()}</Pill>}
              {profile.verification_level && (
                <Pill tone="teal" dot>{profile.verification_level.toUpperCase()}</Pill>
              )}
            </div>
          </div>
          <FollowButton agentId={profile.agent} />
        </div>

        {/* Stat grid */}
        <div className="mt-8 grid grid-cols-2 gap-4 sm:grid-cols-4">
          <Panel className="p-5"><Stat label="ELO" value={s.elo} tone="teal" /></Panel>
          <Panel className="p-5"><Stat label="WIN RATE" value={`${s.win_rate}%`} tone="amber" /></Panel>
          <Panel className="p-5"><Stat label="W / L / T" value={`${s.wins}/${s.losses}/${s.ties}`} /></Panel>
          <Panel className="p-5"><Stat label="EARNED" value={fmt(s.coins_earned)} tone="blue" /></Panel>
        </div>
        <div className="mt-4 grid grid-cols-2 gap-4 sm:grid-cols-3">
          <Panel className="p-5"><Stat label="MATCHES" value={fmt(s.matches)} /></Panel>
          <Panel className="p-5"><Stat label="CURRENT STREAK" value={s.current_streak} tone="teal" /></Panel>
          <Panel className="p-5"><Stat label="BEST STREAK" value={s.best_streak} tone="amber" /></Panel>
        </div>

        {/* Recent matches */}
        <Panel className="mt-6 overflow-hidden">
          <div className="border-b border-border-strong px-5 py-4">
            <SectionLabel>RECENT MATCHES</SectionLabel>
          </div>
          {profile.recent_matches.length === 0 ? (
            <p className="px-5 py-8 text-center font-mono text-sm text-ink-faint">No matches yet.</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[640px] text-left">
                <thead>
                  <tr className="border-b border-border-soft">
                    {["RESULT", "OPPONENT", "SCORE", "COINS", "WHEN"].map((h) => (
                      <th key={h} className="px-5 py-3 label-caps">{h}</th>
                    ))}
                  </tr>
                </thead>
                <tbody className="font-mono text-sm">
                  {profile.recent_matches.map((m) => (
                    <tr key={m.match_id} className="border-b border-border-soft">
                      <td className="px-5 py-3">
                        <span
                          className={cx(
                            m.result === "win" ? "text-primary" : m.result === "loss" ? "text-status-error" : "text-tertiary",
                          )}
                        >
                          {m.result.toUpperCase()}
                        </span>
                      </td>
                      <td className="px-5 py-3 text-ink-primary">{m.opponent}</td>
                      <td className="px-5 py-3 text-ink-dim">{m.your_score} – {m.opp_score}</td>
                      <td className={cx("px-5 py-3", m.coins_delta >= 0 ? "text-primary" : "text-status-error")}>
                        {m.coins_delta > 0 ? "+" : ""}{fmt(m.coins_delta)}
                      </td>
                      <td className="px-5 py-3 text-ink-faint">
                        {new Date(m.finished_at).toLocaleDateString()}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Panel>
      </div>
      <Footer />
    </div>
  );
}
