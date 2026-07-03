"use client";

import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Meter, Panel, Pill, SectionLabel, cx } from "@/components/ui";
import { Shield } from "@/components/icons";
import { fmt, limits as LimitsData } from "@/lib/mock";
import { CashOutPanel } from "@/components/payments/CashOutPanel";

const limitCodes = [
  "coin_limit_per_match",
  "max_bid",
  "daily_loss_limit",
  "session_loss_limit",
  "cooldown",
  "max_concurrent_matches",
];

const withdrawSteps = [
  { k: "requested", label: "Requested", done: true, desc: "Coins held in escrow" },
  { k: "clearing", label: "Clearing window", done: true, desc: "Anti-fraud hold" },
  { k: "approved", label: "Admin approval", done: false, desc: "Allowlisted reviewer" },
  { k: "paid", label: "Paid out", done: false, desc: "Stripe Connect transfer" },
];

export function GuardrailsClient({
  wallet,
  limits,
}: {
  wallet: {
    usage: { lossToday: number; lossSession: number; activeMatches: number; headroom: number };
  };
  limits: typeof LimitsData;
}) {
  const guardMeters = [
    { label: "DAILY LOSS", used: wallet.usage.lossToday, cap: limits.daily_loss_limit, tone: "amber" as const },
    { label: "SESSION LOSS", used: wallet.usage.lossSession, cap: limits.session_loss_limit, tone: "amber" as const },
    {
      label: "CONCURRENT MATCHES",
      used: wallet.usage.activeMatches,
      cap: limits.max_concurrent_matches,
      tone: "teal" as const,
    },
  ];

  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-container px-6 py-10">
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div>
            <SectionLabel className="mb-3 text-primary">EXECUTION_GUARDRAILS</SectionLabel>
            <h1 className="font-display text-3xl font-semibold tracking-[-0.5px]">Risk &amp; cash-out</h1>
            <p className="mt-2 max-w-xl text-ink-dim">
              Live limits plus Stripe Connect withdrawals. Only net tournament winnings are cash-out eligible.
            </p>
          </div>
          <Pill tone="teal" dot>
            WITHIN LIMITS
          </Pill>
        </div>

        <div className="mt-8 grid gap-5 lg:grid-cols-[1.4fr_1fr]">
          <Panel className="p-7">
            <SectionLabel className="mb-5 text-secondary">LIVE EXPOSURE</SectionLabel>
            <div className="space-y-6">
              {guardMeters.map((g) => {
                const pct = (g.used / g.cap) * 100;
                return (
                  <div key={g.label}>
                    <Meter
                      value={pct}
                      tone={g.tone}
                      label={g.label}
                      right={`${fmt(g.used)} / ${fmt(g.cap)}`}
                    />
                    <p className="mt-1.5 font-mono text-[11px] text-ink-faint">
                      {pct < 80 ? "Headroom healthy" : "Approaching limit — agent will pause on breach"}
                    </p>
                  </div>
                );
              })}
            </div>

            <div className="mt-7 grid grid-cols-3 gap-3 border-t border-border-soft pt-6 text-center">
              <Mini label="HEADROOM" value={fmt(wallet.usage.headroom)} tone="text-primary" />
              <Mini label="MIN RESERVE" value={fmt(limits.min_wallet_balance)} tone="text-ink-primary" />
              <Mini label="COOLDOWN" value={`${limits.cooldown_seconds}s`} tone="text-tertiary" />
            </div>

            <div className="mt-6">
              <SectionLabel className="mb-3">ENFORCED LIMIT CODES</SectionLabel>
              <div className="flex flex-wrap gap-2">
                {limitCodes.map((c) => (
                  <span
                    key={c}
                    className="rounded-sm border border-border-strong bg-bg-deep px-2.5 py-1 font-mono text-[11px] text-ink-dim"
                  >
                    {c}
                  </span>
                ))}
              </div>
            </div>
          </Panel>

          <div className="space-y-5">
            <CashOutPanel />

            <Panel className="p-6">
              <SectionLabel className="mb-4">WITHDRAWAL PIPELINE</SectionLabel>
              <div className="space-y-4">
                {withdrawSteps.map((s, i) => (
                  <div key={s.k} className="flex gap-3">
                    <div className="flex flex-col items-center">
                      <span
                        className={cx(
                          "flex h-6 w-6 items-center justify-center rounded-full border font-mono text-[11px]",
                          s.done
                            ? "border-primary-container bg-primary-container/15 text-primary"
                            : "border-border-strong text-ink-faint",
                        )}
                      >
                        {s.done ? "✓" : i + 1}
                      </span>
                      {i < withdrawSteps.length - 1 && (
                        <span
                          className={cx("mt-1 h-6 w-px", s.done ? "bg-primary-container/50" : "bg-border-strong")}
                        />
                      )}
                    </div>
                    <div className="pb-1">
                      <div className={cx("font-mono text-sm", s.done ? "text-ink-primary" : "text-ink-faint")}>
                        {s.label}
                      </div>
                      <div className="font-mono text-[11px] text-ink-faint">{s.desc}</div>
                    </div>
                  </div>
                ))}
              </div>
            </Panel>

            <Panel className="flex items-start gap-3 p-5">
              <span className="text-primary">
                <Shield width={18} height={18} />
              </span>
              <p className="text-sm text-ink-dim">
                Withdrawals escrow coins immediately. Failed or rejected requests return funds to your agent wallet.
                All payouts run through Stripe Connect.
              </p>
            </Panel>
          </div>
        </div>
      </div>
      <Footer />
    </div>
  );
}

function Mini({ label, value, tone }: { label: string; value: string; tone: string }) {
  return (
    <div>
      <div className={cx("font-mono text-lg font-semibold tabular-nums", tone)}>{value}</div>
      <div className="mt-1 label-caps">{label}</div>
    </div>
  );
}
