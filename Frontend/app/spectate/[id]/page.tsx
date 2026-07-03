import Link from "next/link";
import { TopNav } from "@/components/Nav";
import { GameCard, Meter, Panel, Pill, SectionLabel, Button, cx } from "@/components/ui";
import { Lock, Eye, Cpu, Brain, Trophy, Coin, Layers, ChevronLeft } from "@/components/icons";
import { fmt } from "@/lib/mock";
import { fetchSpectate } from "@/lib/api";
import { SpectateLive } from "../SpectateLive";

export default async function SpectateMatchPage({
  params,
}: {
  params: { id: string };
}) {
  const { liveMatch, recentBids } = await fetchSpectate();
  // The URL match id drives the live SSE feed; the header view is seeded from
  // the arena feed (fetchSpectate), which resolves the current live table.
  const matchId = decodeURIComponent(params.id);
  const m = liveMatch;

  const scoreA = m.agentA.score;
  const scoreB = m.agentB.score;
  const lead = Math.abs(scoreA - scoreB);
  const leader: "A" | "B" | null = scoreA > scoreB ? "A" : scoreB > scoreA ? "B" : null;
  const shortA = m.agentA.name.replace(/^AGENT[_ ]?/i, "");
  const shortB = m.agentB.name.replace(/^AGENT[_ ]?/i, "");
  const progress = Math.round((m.round / m.totalRounds) * 100);

  return (
    // Theme follows the site-wide light/dark toggle (the `.broadcast` scope is
    // applied on <body>), so the broadcast renders in both light and dark.
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto w-full max-w-[1400px] px-6 py-8 md:px-8">
        {/* ── Back to the watch hub ──────────────────────────────────── */}
        <Link
          href="/spectate"
          className="mb-5 inline-flex items-center gap-1.5 font-mono text-[11px] uppercase tracking-caps text-ink-dim transition hover:text-ink-primary"
        >
          <ChevronLeft width={14} height={14} /> All live matches
        </Link>

        {/* ── Header ─────────────────────────────────────────────────── */}
        <div className="flex flex-wrap items-start justify-between gap-6">
          <div>
            <Pill tone="red" dot className="mb-3">
              LIVE MATCH · {m.tournament}
            </Pill>
            <h1 className="font-display text-3xl font-semibold tracking-[-0.5px] md:text-4xl">
              Goofspiel <span className="text-ink-faint">/</span>{" "}
              <span className="text-secondary">High Stakes</span>
            </h1>
            <p className="mt-1.5 font-mono text-[12px] text-ink-faint">
              Probability-of-carryover engine · provably fair seed committed
            </p>
          </div>
          <div className="flex gap-3">
            <HudStat label="ROUND" value={`${String(m.round).padStart(2, "0")} / ${m.totalRounds}`} icon={<Layers width={14} height={14} />} />
            <HudStat label="PRIZE POT" value={fmt(m.pot)} tone="amber" icon={<Coin width={14} height={14} />} />
          </div>
        </div>

        {/* ── Hero scoreboard — who's winning at a glance ────────────── */}
        <Panel glass className="card-elev-lg mt-6 overflow-hidden p-5 md:p-7">
          <div className="grid items-center gap-5 md:grid-cols-[1fr_auto_1fr]">
            <AgentScore
              name={m.agentA.name}
              version={m.agentA.version}
              score={scoreA}
              tone="teal"
              leading={leader === "A"}
              align="left"
            />

            {/* Center column: VS, lead margin, round progress */}
            <div className="flex flex-col items-center gap-3 px-2">
              <span className="font-display text-sm font-semibold tracking-[3px] text-ink-faint">
                VS
              </span>
              {leader ? (
                <Pill tone={leader === "A" ? "teal" : "amber"} dot>
                  {(leader === "A" ? shortA : shortB)} +{lead}
                </Pill>
              ) : (
                <Pill tone="neutral">TIED · {scoreA}</Pill>
              )}
              <div className="w-40">
                <div className="mb-1.5 flex items-center justify-between">
                  <span className="label-caps">PROGRESS</span>
                  <span className="font-mono text-[11px] text-ink-dim">{progress}%</span>
                </div>
                <div className="h-1.5 w-full overflow-hidden rounded-full bg-bg-deep">
                  <div
                    className="h-full rounded-full bg-primary-container shadow-glow-teal"
                    style={{ width: `${progress}%` }}
                  />
                </div>
                <p className="mt-1.5 text-center font-mono text-[11px] text-ink-faint">
                  {m.totalRounds - m.round} rounds left
                </p>
              </div>
            </div>

            <AgentScore
              name={m.agentB.name}
              version={m.agentB.version}
              score={scoreB}
              tone="amber"
              leading={leader === "B"}
              align="right"
            />
          </div>
        </Panel>

        {/* ── How the game works — explainer ─────────────────────────── */}
        <Panel className="mt-5 p-4">
          <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
            <div className="flex shrink-0 items-center gap-2 text-primary">
              <Brain width={16} height={16} />
              <SectionLabel className="text-primary">HOW GOOFSPIEL WORKS</SectionLabel>
            </div>
            <p className="min-w-[260px] flex-1 font-mono text-[12.5px] leading-6 text-ink-dim">
              Each round a <span className="text-secondary">prize card</span> is revealed. Both
              agents secretly <span className="text-primary">bid</span> one card from their hand —
              the higher bid wins the prize. A tie makes the prize{" "}
              <span className="text-tertiary">carry over</span> to the next round. Highest score
              after {m.totalRounds} rounds takes the pot.
            </p>
          </div>
        </Panel>

        {/* ── Arena: 3-column split ──────────────────────────────────── */}
        <div className="mt-5 grid gap-5 lg:grid-cols-[1fr_minmax(280px,1.05fr)_1fr]">
          {/* Agent A */}
          <AgentPanel
            name={m.agentA.name}
            version={m.agentA.version}
            leading={leader === "A"}
            meter={{ value: m.agentA.aggression, tone: "teal", label: "AGGRESSION" }}
            tone="teal"
            align="left"
          />

          {/* Prize + sealed bids */}
          <Panel glass className="flex flex-col items-center p-5">
            <SectionLabel className="text-secondary">PRIZE THIS ROUND</SectionLabel>
            <div className="mt-3 w-40">
              <GameCard
                value={"J"}
                suit={m.prizeSuit}
                label="VALUE"
                size="lg"
                active
                className="border-secondary/60 shadow-glow-amber"
              />
            </div>
            <p className="mt-3 text-center font-mono text-[12px] text-ink-faint">
              Diamonds are wild this round
            </p>

            <div className="mt-6 w-full">
              <div className="mb-2 flex items-center justify-center gap-2 text-ink-faint">
                <Lock width={13} height={13} />
                <span className="label-caps">SEALED BIDS · REVEALED AT SHOWDOWN</span>
              </div>
              <div className="grid w-full grid-cols-2 gap-3">
                {[
                  { label: shortA, tone: "teal" as const },
                  { label: shortB, tone: "amber" as const },
                ].map((b) => (
                  <div key={b.label} className="text-center">
                    <div className="mb-1.5 flex items-center justify-center">
                      <span
                        className={cx(
                          "font-mono text-[11px] font-medium uppercase tracking-caps",
                          b.tone === "teal" ? "text-primary" : "text-secondary",
                        )}
                      >
                        {b.label}
                      </span>
                    </div>
                    <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-border-strong bg-bg-deep bg-redact py-6">
                      <Lock width={18} height={18} />
                      <span className="label-caps">SEALED</span>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          </Panel>

          {/* Agent B */}
          <AgentPanel
            name={m.agentB.name}
            version={m.agentB.version}
            leading={leader === "B"}
            meter={{ value: m.agentB.efficiency, tone: "amber", label: "EFFICIENCY" }}
            tone="amber"
            align="right"
          />
        </div>

        {/* ── Recent results ─────────────────────────────────────────── */}
        <Panel className="mt-5 p-5">
          <div className="mb-4 flex items-center justify-between">
            <div className="flex items-center gap-2 text-ink-dim">
              <Trophy width={15} height={15} />
              <SectionLabel>RECENT RESULTS</SectionLabel>
            </div>
            <span className="font-mono text-[11px] text-ink-faint">
              LAST {recentBids.length} ROUNDS
            </span>
          </div>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            {recentBids.map((b) => (
              <div
                key={b.round}
                className="hover-lift rounded-lg border border-border-soft bg-surface-slate/40 p-3.5"
              >
                <div className="flex items-center justify-between">
                  <span className="label-caps">ROUND {String(b.round).padStart(2, "0")}</span>
                  <WinnerTag winner={b.winner} />
                </div>
                <div className="mt-3 flex items-center justify-between font-mono tabular-nums">
                  <span className="text-xl font-semibold text-primary">
                    {String(b.a).padStart(2, "0")}
                  </span>
                  <span className="text-[11px] text-ink-faint">vs</span>
                  <span className="text-xl font-semibold text-secondary">
                    {String(b.b).padStart(2, "0")}
                  </span>
                </div>
                <div className="mt-2 border-t border-border-soft pt-2 text-center font-mono text-[10px] uppercase tracking-caps text-ink-faint">
                  Prize value {b.prize}
                </div>
              </div>
            ))}
          </div>
        </Panel>

        {/* ── Realtime SSE feed ──────────────────────────────────────── */}
        <SpectateLive matchId={matchId} />

        {/* ── CTA ────────────────────────────────────────────────────── */}
        <div className="mt-5 flex justify-center">
          <Button href={`/goofspiel?match=${encodeURIComponent(matchId)}`} variant="primary">
            <Eye width={15} height={15} /> Open strategy theater
          </Button>
        </div>

        {/* ── Commentary ─────────────────────────────────────────────── */}
        <Panel glass className="mt-5 flex items-center gap-4 p-4">
          <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-primary-container/40 bg-primary-container/10 text-primary">
            <Cpu width={16} height={16} />
          </span>
          <p className="flex-1 font-mono text-[13px] leading-6 text-ink-dim">
            <span className="text-ink-faint">[{m.commentaryTs}]</span> {m.commentary}
          </p>
          <span className="animate-pulse text-ink-faint">↻</span>
        </Panel>
      </div>
    </div>
  );
}

/* ── Header HUD stat ──────────────────────────────────────────────── */
function HudStat({
  label,
  value,
  tone,
  icon,
}: {
  label: string;
  value: string;
  tone?: "amber";
  icon?: React.ReactNode;
}) {
  return (
    <div className="rounded-lg border border-border-strong bg-surface-slate/60 px-5 py-3 text-center">
      <div
        className={cx(
          "font-mono text-lg font-semibold tabular-nums",
          tone === "amber" ? "text-secondary" : "text-ink-primary",
        )}
      >
        {value}
      </div>
      <div className="mt-1 flex items-center justify-center gap-1.5 text-ink-faint">
        {icon}
        <span className="label-caps">{label}</span>
      </div>
    </div>
  );
}

/* ── Scoreboard agent (big score + leading state) ─────────────────── */
function AgentScore({
  name,
  version,
  score,
  tone,
  leading,
  align,
}: {
  name: string;
  version: string;
  score: number;
  tone: "teal" | "amber";
  leading: boolean;
  align: "left" | "right";
}) {
  const right = align === "right";
  const accent = tone === "teal" ? "text-primary" : "text-secondary";
  return (
    <div
      className={cx(
        "relative rounded-lg border p-4 transition md:p-5",
        leading
          ? tone === "teal"
            ? "border-primary/50 bg-primary-container/[0.06] shadow-glow-teal-lg"
            : "border-secondary/50 bg-secondary/[0.06] shadow-glow-amber"
          : "border-border-soft",
      )}
    >
      <div className={cx("flex items-center gap-3", right && "flex-row-reverse")}>
        <span
          className={cx(
            "grid h-9 w-9 shrink-0 place-items-center rounded-md border border-border-strong bg-bg-deep",
            accent,
          )}
        >
          <Cpu width={16} height={16} />
        </span>
        <div className={cx(right && "text-right")}>
          <div className="font-display text-base font-semibold leading-tight text-ink-primary">
            {name}
          </div>
          <div className="font-mono text-[11px] text-ink-faint">VER {version}</div>
        </div>
      </div>
      <div className={cx("mt-3 flex items-baseline gap-3", right && "flex-row-reverse")}>
        <span
          className={cx(
            "font-mono text-5xl font-semibold tabular-nums leading-none md:text-6xl",
            leading ? accent : "text-ink-primary",
          )}
        >
          {score}
        </span>
        {leading && (
          <Pill tone={tone} dot>
            LEADING
          </Pill>
        )}
      </div>
    </div>
  );
}

/* ── Arena agent panel (hand + meter) ─────────────────────────────── */
function AgentPanel({
  name,
  version,
  leading,
  meter,
  tone,
  align,
}: {
  name: string;
  version: string;
  leading: boolean;
  meter: { value: number; tone: "teal" | "amber"; label: string };
  tone: "teal" | "amber";
  align: "left" | "right";
}) {
  const right = align === "right";
  const accent = tone === "teal" ? "text-primary" : "text-secondary";
  return (
    <Panel glass className="p-5">
      <div className={cx("flex items-start justify-between gap-2", right && "flex-row-reverse text-right")}>
        <div className={cx(right && "text-right")}>
          <SectionLabel className={accent}>{name}</SectionLabel>
          <div className="mt-0.5 font-mono text-[11px] text-ink-faint">VER {version}</div>
        </div>
        {leading && (
          <Pill tone={tone} dot>
            LEADING
          </Pill>
        )}
      </div>

      <div className="mt-5">
        <SectionLabel className="mb-2">HAND [REDACTED]</SectionLabel>
        <div className="grid grid-cols-4 gap-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <div
              key={i}
              className="aspect-[3/4] rounded-md border border-border-strong bg-bg-deep bg-redact"
            />
          ))}
        </div>
      </div>

      <div className="mt-5">
        <Meter
          value={meter.value}
          tone={meter.tone}
          label={meter.label}
          right={`${meter.value}%`}
        />
      </div>
    </Panel>
  );
}

/* ── Winner tag for recent results ────────────────────────────────── */
function WinnerTag({ winner }: { winner: string }) {
  const tone = winner.includes("A") ? "teal" : winner.includes("B") ? "amber" : "neutral";
  return <Pill tone={tone}>{winner}</Pill>;
}
