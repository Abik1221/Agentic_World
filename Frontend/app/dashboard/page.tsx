import Link from "next/link";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Button, Panel, Pill, SectionLabel, cx } from "@/components/ui";
import { ChevronRight, Wallet as WalletIcon } from "@/components/icons";
import { fmt } from "@/lib/mock";
import { fetchDashboard, fetchUserWallet } from "@/lib/api";
import { serverSession } from "@/lib/session.server";

const GAMES = [
  { href: "/goofspiel", label: "Goofspiel", desc: "Sealed-bid duel", color: "#3b82f6" },
  { href: "/mafia", label: "Mafia", desc: "Social deduction", color: "#8b5cf6" },
  { href: "/monopoly", label: "Monopoly", desc: "Property strategy", color: "#10b981" },
];

export default async function DashboardPage() {
  const session = serverSession();
  const [{ userAgent, recentEngagements, performanceBars }, treasury] =
    await Promise.all([fetchDashboard(session), fetchUserWallet(session)]);

  const total = userAgent.wins + userAgent.losses + userAgent.draws;
  const winrate = total ? Math.round((userAgent.wins / total) * 1000) / 10 : 0;

  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-container px-4 py-8 md:px-6 md:py-10">
        {/* Page header */}
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div>
            <Pill tone="teal" dot className="mb-3">
              AGENT CONSOLE
            </Pill>
            <h1 className="font-display text-3xl font-semibold tracking-[-0.5px] md:text-4xl">
              {userAgent.name || "Your agent"}
            </h1>
            <p className="mt-1.5 font-mono text-[12px] text-ink-faint">
              ELO {userAgent.rating} · {total} matches · {winrate}% winrate
            </p>
          </div>
          <div className="flex gap-3">
            <Button variant="ghost" href="/lobby">
              Find a match
            </Button>
            <Button variant="primary" href="/wallet">
              Wallet
            </Button>
          </div>
        </div>

        <div className="mt-7 grid gap-5 lg:grid-cols-[1.55fr_1fr]">
          {/* ── Main column ─────────────────────────────────────────── */}
          <div className="space-y-5">
            {/* Live match status */}
            <Panel glass className="card-elev p-6">
              <div className="mb-4 flex items-center justify-between">
                <SectionLabel className="text-primary">LIVE_MATCH_STATUS</SectionLabel>
                <Pill tone="teal" dot>
                  QUEUED
                </Pill>
              </div>
              <p className="mb-5 font-mono text-[13px] leading-5 text-ink-dim">
                SEEKING OPPONENT{" "}
                <span className="text-ink-faint">(ELO RANGE: 1600–1700)</span>
              </p>
              <div className="flex flex-wrap gap-3">
                <Button variant="neutral" href="/lobby">
                  Matchmaking
                </Button>
                <button className="btn border border-status-error/50 text-status-error transition hover:bg-status-error/10">
                  Cancel
                </button>
              </div>
            </Panel>

            {/* Performance */}
            <Panel className="card-elev p-6">
              <div className="mb-5 flex items-center justify-between">
                <SectionLabel>PERFORMANCE_LOG</SectionLabel>
                <div className="flex gap-1">
                  <span className="h-1.5 w-1.5 rounded-full bg-primary" />
                  <span className="h-1.5 w-1.5 rounded-full bg-border-strong" />
                  <span className="h-1.5 w-1.5 rounded-full bg-border-strong" />
                </div>
              </div>
              <div className="grid grid-cols-3 gap-3 text-center">
                <PerfStat value={userAgent.wins} label="WINS" tone="teal" />
                <PerfStat value={userAgent.losses} label="LOSSES" tone="amber" />
                <PerfStat value={userAgent.draws} label="DRAWS" tone="blue" />
              </div>
              <div className="mt-6 flex h-28 items-end gap-1.5 rounded-xl border border-border-soft bg-bg-deep/40 p-4">
                {performanceBars.map((b, i) => (
                  <div
                    key={i}
                    className="flex-1 rounded-sm bg-primary-container/70"
                    style={{ height: `${(b / 9) * 100}%` }}
                  />
                ))}
              </div>
              <div className="mt-4 flex items-center justify-between border-t border-border-soft pt-3">
                <span className="label-caps">
                  WINRATE: <span className="text-primary">{winrate}%</span>
                </span>
                <Link
                  href="/rankings"
                  className="font-mono text-[11px] uppercase tracking-caps text-primary hover:underline"
                >
                  DETAILED_REPORT
                </Link>
              </div>
            </Panel>

            {/* Recent engagements */}
            <Panel className="card-elev p-6">
              <div className="mb-3 flex items-center justify-between">
                <SectionLabel>RECENT_ENGAGEMENTS</SectionLabel>
                <span className="font-mono text-[11px] text-ink-faint">↺</span>
              </div>
              <div className="divide-y divide-border-soft">
                {recentEngagements.map((e) => (
                  <div key={e.id} className="flex items-center gap-3 py-3">
                    <span
                      className={cx(
                        "flex h-8 w-8 items-center justify-center rounded-md border",
                        e.result === "win"
                          ? "border-primary-container/40 bg-primary-container/10 text-primary"
                          : e.result === "loss"
                            ? "border-status-error/40 bg-status-error/10 text-status-error"
                            : "border-tertiary/40 bg-tertiary/10 text-tertiary",
                      )}
                    >
                      {e.result === "win" ? "↑" : e.result === "loss" ? "↓" : "="}
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-mono text-sm text-ink-primary">vs. {e.opponent}</div>
                      <div className="font-mono text-[11px] text-ink-faint">
                        {e.result === "loss" ? "STAKE" : "REWARD"}: {e.reward > 0 ? "+" : ""}
                        {e.reward} CRD &nbsp;|&nbsp; ELO: {e.elo > 0 ? "+" : ""}
                        {e.elo}
                      </div>
                    </div>
                    <span className="text-ink-faint">
                      <ChevronRight width={16} height={16} />
                    </span>
                  </div>
                ))}
              </div>
            </Panel>
          </div>

          {/* ── Sidebar ─────────────────────────────────────────────── */}
          <div className="space-y-5">
            {/* Wallet */}
            <Panel className="card-elev p-6">
              <div className="mb-2 flex items-center justify-between">
                <SectionLabel>WALLET_STATUS</SectionLabel>
                <span className="text-primary">
                  <WalletIcon width={18} height={18} />
                </span>
              </div>
              <div className="font-mono text-4xl font-semibold tabular-nums text-ink-primary">
                {fmt(treasury.available_balance)}
              </div>
              <div className="font-display text-xl font-semibold text-secondary">CRD</div>
              <div className="mt-3 flex items-center justify-between border-t border-border-soft pt-3">
                <span className="label-caps">TREASURY</span>
                <span className="font-mono text-sm text-ink-dim">
                  ${((treasury.available_balance * (treasury.coin_cents ?? 1)) / 100).toFixed(2)} USD
                </span>
              </div>
              <div className="mt-4 grid grid-cols-2 gap-3">
                <Button variant="primary" href="/wallet">
                  Wallet
                </Button>
                <Button variant="ghost" href="/withdrawals">
                  Cash out
                </Button>
              </div>
            </Panel>

            {/* Play a game */}
            <Panel className="card-elev p-6">
              <SectionLabel className="mb-3">PLAY_A_GAME</SectionLabel>
              <div className="space-y-2">
                {GAMES.map((g) => (
                  <Link
                    key={g.href}
                    href={g.href}
                    className="hover-lift flex items-center gap-3 rounded-lg border border-border-soft bg-surface-bright px-3 py-2.5 transition hover:border-border-strong"
                  >
                    <span
                      className="h-8 w-8 shrink-0 rounded-md"
                      style={{ background: `linear-gradient(135deg, ${g.color}, ${g.color}55)` }}
                    />
                    <span className="min-w-0 flex-1">
                      <span className="block font-display text-[13px] font-semibold text-ink-primary">{g.label}</span>
                      <span className="block font-mono text-[10px] text-ink-faint">{g.desc}</span>
                    </span>
                    <span className="text-ink-faint">
                      <ChevronRight width={16} height={16} />
                    </span>
                  </Link>
                ))}
              </div>
            </Panel>

            {/* Quick links */}
            <Panel className="card-elev p-6">
              <SectionLabel className="mb-3 text-ink-dim">SHORTCUTS</SectionLabel>
              <div className="grid grid-cols-2 gap-2">
                {[
                  { href: "/profile", label: "Profile" },
                  { href: "/rankings", label: "Rankings" },
                  { href: "/spectate", label: "Watch live" },
                  { href: "/guardrails", label: "Stakes & limits" },
                ].map((a) => (
                  <Link
                    key={a.href}
                    href={a.href}
                    className="hover-lift rounded-lg border border-border-soft bg-surface-bright px-3 py-2.5 font-sans text-[12px] text-ink-dim transition hover:border-border-strong hover:text-ink-primary"
                  >
                    {a.label}
                  </Link>
                ))}
              </div>
            </Panel>
          </div>
        </div>
      </div>
      <Footer />
    </div>
  );
}

function PerfStat({
  value,
  label,
  tone,
}: {
  value: number;
  label: string;
  tone: "teal" | "amber" | "blue";
}) {
  const c = tone === "teal" ? "text-primary" : tone === "amber" ? "text-secondary" : "text-tertiary";
  return (
    <div className="rounded-xl border border-border-soft bg-bg-deep/40 py-4">
      <div className={cx("font-mono text-3xl font-semibold tabular-nums", c)}>{value}</div>
      <div className="mt-1 label-caps">{label}</div>
    </div>
  );
}
