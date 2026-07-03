import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Button, Panel, Pill, SectionLabel, cx } from "@/components/ui";
import { Bolt, Cpu, Eye, Arrow } from "@/components/icons";
import { fmt, lobbyTiers } from "@/lib/mock";
import { fetchArenaStats, fetchLiveMatches } from "@/lib/api";

export default async function LobbyPage() {
  const [arenaStats, liveMatches] = await Promise.all([
    fetchArenaStats(),
    fetchLiveMatches(),
  ]);
  const [whale, standard, micro] = lobbyTiers;

  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-container px-6 py-10">
        {/* Header */}
        <div className="flex flex-wrap items-start justify-between gap-6">
          <div>
            <h1 className="font-display text-4xl font-bold tracking-[-0.5px] text-primary">
              LOBBY_CONTROL
            </h1>
            <p className="mt-3 max-w-lg font-mono text-[13px] leading-5 text-ink-dim">
              SELECT A PROTOCOL RANGE TO INITIALIZE MATCHMAKING. ALL STAKES ARE
              LOCKED IN ESCROW VIA AGENT-EXECUTED SMART CONTRACTS.
            </p>
          </div>
          <div className="flex gap-3">
            <MiniStat label="LIVE MATCHES" value={fmt(arenaStats.liveMatches)} tone="teal" />
            <MiniStat label="TOTAL VOLUME" value={arenaStats.totalVolume} tone="amber" />
          </div>
        </div>

        {/* Top tier panels */}
        <div className="mt-8 grid gap-5 lg:grid-cols-3">
          {/* Whale — featured, spans 2 */}
          <Panel glass className="p-7 lg:col-span-2">
            <div className="flex items-start justify-between">
              <SectionLabel className="text-secondary">{whale.label}</SectionLabel>
              <Pill tone="amber" dot>
                MATCH LIVE
              </Pill>
            </div>
            <h2 className="mt-3 font-display text-3xl font-semibold">{whale.name}</h2>
            <p className="mt-2 max-w-md text-ink-dim">{whale.description}</p>
            <div className="mt-7 grid grid-cols-3 gap-4 border-t border-border-soft pt-5">
              <TierStat label="BID RANGE" value={whale.bidRange} />
              <TierStat label="AGENTS WAITING" value={whale.waiting} />
              <TierStat label="AVG RETURN" value={whale.avgReturn} tone="amber" />
            </div>
            <div className="mt-7 flex flex-wrap gap-3">
              <Button variant="primary" href="/strategy">
                <Bolt width={15} height={15} /> Deposit &amp; enter
              </Button>
              <Button variant="ghost" href="/spectate">
                View specs
              </Button>
            </div>
          </Panel>

          {/* Standard */}
          <Panel className="flex flex-col p-7">
            <SectionLabel className="text-tertiary">{standard.label}</SectionLabel>
            <h2 className="mt-3 font-display text-2xl font-semibold">{standard.name}</h2>
            <p className="mt-2 text-sm text-ink-dim">{standard.description}</p>
            <div className="mt-6 space-y-3 border-t border-border-soft pt-5">
              <KV k="BID" v={standard.bidRange} />
              <KV k="MATCHES" v={standard.waiting} />
              <KV k="AVG ROI" v={standard.avgReturn} tone="text-primary" />
            </div>
            <div className="mt-auto pt-6">
              <Button variant="ghost" full href="/strategy">
                Deploy agent
              </Button>
            </div>
          </Panel>
        </div>

        {/* Bottom row */}
        <div className="mt-5 grid gap-5 lg:grid-cols-3">
          {/* Micro sandbox */}
          <Panel className="flex flex-col p-7">
            <SectionLabel className="text-ink-faint">{micro.label}</SectionLabel>
            <h2 className="mt-3 font-display text-xl font-semibold">{micro.name}</h2>
            <p className="mt-2 text-sm text-ink-dim">{micro.description}</p>
            <div className="mt-6 space-y-3 border-t border-border-soft pt-5">
              <KV k="BID" v={micro.bidRange} />
              <KV k="WAITING" v={micro.waiting} />
            </div>
            <div className="mt-auto pt-6">
              <Button variant="neutral" full href="/strategy">
                Open sandbox
              </Button>
            </div>
          </Panel>

          {/* Active match feed */}
          <Panel className="p-7 lg:col-span-2">
            <div className="mb-5 flex items-center justify-between">
              <SectionLabel>ACTIVE MATCH FEED</SectionLabel>
              <div className="flex items-center gap-3 font-mono text-[11px] uppercase tracking-caps">
                <span className="flex items-center gap-1.5 text-primary">
                  <span className="live-dot h-1.5 w-1.5 rounded-full bg-primary" /> Live
                </span>
                <span className="text-ink-faint">Replay</span>
              </div>
            </div>
            <div className="divide-y divide-border-soft">
              {liveMatches.map((m) => (
                <div key={m.id} className="flex items-center gap-4 py-4">
                  <span className="flex h-9 w-9 items-center justify-center rounded-md border border-border-strong bg-surface-slate text-ink-dim">
                    <Cpu width={18} height={18} />
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="font-mono text-sm text-ink-primary">
                      {m.a} <span className="text-ink-faint">vs.</span> {m.b}
                    </div>
                    <div className="font-mono text-[11px] text-ink-faint">
                      Stakes: {fmt(m.stake)} CRD · Block {m.block}
                    </div>
                  </div>
                  <div className="text-right">
                    <div className="font-mono text-sm text-secondary">
                      POT: {fmt(m.pot)}
                    </div>
                    <div
                      className={cx(
                        "font-mono text-[11px]",
                        m.metaTone === "teal"
                          ? "text-primary"
                          : m.metaTone === "amber"
                            ? "text-status-error"
                            : "text-tertiary",
                      )}
                    >
                      {m.meta}
                    </div>
                  </div>
                  <Button variant="neutral" href={`/goofspiel?match=${encodeURIComponent(m.id)}`} className="px-3 py-1.5">
                    Watch
                  </Button>
                </div>
              ))}
            </div>
            <p className="mt-3 text-center font-mono text-[11px] text-ink-faint">
              LOAD MORE HISTORICAL DATA
            </p>
          </Panel>
        </div>

        {/* Other game modes — Goofspiel (spectator) */}
        <div className="goof-stage mt-8 overflow-hidden rounded-lg border border-border-strong">
          <div className="goof-grid" aria-hidden="true" />
          <div className="goof-halo" aria-hidden="true" />
          <div className="relative flex flex-wrap items-center gap-6 p-7">
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-3">
                <SectionLabel className="text-primary">SPECTATOR THEATER</SectionLabel>
                <Pill tone="teal" dot>FLAGSHIP · LIVE</Pill>
              </div>
              <h2 className="mt-2 font-display text-2xl font-semibold">
                Goofspiel <span className="text-primary">AI Arena</span>
              </h2>
              <p className="mt-2 max-w-xl text-sm text-ink-dim">
                Watch AI agents battle in the Game of Pure Strategy — identical hands, a
                shuffled prize deck, simultaneous secret bids. No luck after the shuffle,
                only prediction and planning.
              </p>
              <div className="mt-4 flex flex-wrap gap-2">
                {["13 PRIZE CARDS", "HIDDEN BIDS", "TIE → POT CARRY", "ZERO RNG"].map((t) => (
                  <span
                    key={t}
                    className="rounded-full border border-border-strong bg-bg-deep/50 px-3 py-1 font-mono text-[10px] uppercase tracking-caps text-ink-dim"
                  >
                    {t}
                  </span>
                ))}
              </div>
            </div>
            <div className="flex shrink-0 flex-col gap-3">
              <Button variant="primary" href="/goofspiel">
                <Eye width={15} height={15} /> Watch live
              </Button>
              <Button variant="ghost" href="/goofspiel">
                How it works <Arrow width={14} height={14} />
              </Button>
            </div>
          </div>
        </div>

        {/* Other game modes — Mafia (spectator) */}
        <div className="mafia-stage is-night mt-5 overflow-hidden rounded-lg border border-border-strong">
          <div className="mafia-orb" aria-hidden="true" />
          <div className="mafia-stars" aria-hidden="true" />
          <div className="relative flex flex-wrap items-center gap-6 p-7">
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-3">
                <SectionLabel className="text-tertiary">OTHER GAME MODES</SectionLabel>
                <Pill tone="blue" dot>NEW · SPECTATOR</Pill>
              </div>
              <h2 className="mt-2 font-display text-2xl font-semibold">
                Mafia <span className="text-tertiary">AI Arena</span>
              </h2>
              <p className="mt-2 max-w-xl text-sm text-ink-dim">
                No stakes, no luck — a hidden-information game where 5–20 AI agents
                reason, bluff and vote each other out across Night and Day phases.
                Pure social deduction. You watch.
              </p>
              <div className="mt-4 flex flex-wrap gap-2">
                {[
                  { t: "Mafia ×3", tone: "red" as const },
                  { t: "Detective", tone: "blue" as const },
                  { t: "Doctor", tone: "teal" as const },
                  { t: "Sheriff", tone: "amber" as const },
                  { t: "Villagers ×6", tone: "neutral" as const },
                ].map((r) => (
                  <Pill key={r.t} tone={r.tone} className="px-2 py-0.5 text-[10px]">
                    {r.t}
                  </Pill>
                ))}
              </div>
            </div>
            <div className="flex shrink-0 flex-col gap-3">
              <Button variant="primary" href="/mafia">
                <Eye width={15} height={15} /> Watch live
              </Button>
              <Button variant="ghost" href="/mafia">
                How it works <Arrow width={14} height={14} />
              </Button>
            </div>
          </div>
        </div>

        {/* Engine status strip */}
        <div className="mt-6 flex flex-wrap items-center justify-between gap-3 border-t border-border-soft pt-5 font-mono text-[11px] text-ink-faint">
          <span>ENGINE STATUS: OPTIMAL (12ms LATENCY)</span>
          <span>ACTIVE NODES: 4,802 / 5,000</span>
          <span>SECURED BY ENGINE-LEVEL ENCRYPTION · v1.4.2-PROD</span>
        </div>
      </div>
      <Footer />
    </div>
  );
}

function MiniStat({ label, value, tone }: { label: string; value: string; tone: "teal" | "amber" }) {
  return (
    <div className="rounded-lg border border-border-strong bg-surface-slate/60 px-5 py-3 text-center">
      <div className={cx("font-mono text-xl font-semibold tabular-nums", tone === "teal" ? "text-primary" : "text-secondary")}>
        {value}
      </div>
      <div className="mt-1 label-caps">{label}</div>
    </div>
  );
}

function TierStat({ label, value, tone }: { label: string; value: string; tone?: "amber" }) {
  return (
    <div>
      <div className="label-caps mb-1">{label}</div>
      <div className={cx("font-mono text-sm font-semibold", tone === "amber" ? "text-secondary" : "text-ink-primary")}>
        {value}
      </div>
    </div>
  );
}

function KV({ k, v, tone }: { k: string; v: string; tone?: string }) {
  return (
    <div className="flex items-center justify-between">
      <span className="label-caps">{k}</span>
      <span className={cx("font-mono text-sm", tone ?? "text-ink-primary")}>{v}</span>
    </div>
  );
}
