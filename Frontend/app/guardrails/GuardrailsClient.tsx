"use client";

import { Activity, CircleDollarSign, Layers, ShieldCheck, Wallet as WalletIcon } from "lucide-react";
import { fmt } from "@/lib/mock";
import { Card, CardHeader, KpiCard, PageHeader, ProgressBar } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";

type Usage = { lossToday: number; lossSession: number; activeMatches: number; headroom: number };
type Wallet = { balance: number; usage: Usage };
type Limits = {
  daily_loss_limit: number;
  session_loss_limit: number;
  max_concurrent_matches: number;
  min_wallet_balance: number;
  cooldown_seconds: number;
};

function meterColor(pct: number) {
  return pct > 80 ? "rgb(239 68 68)" : pct > 55 ? "rgb(245 158 11)" : "rgb(34 197 94)";
}

export function GuardrailsClient({ wallet, limits }: { wallet: Wallet; limits: Limits }) {
  const meters = [
    { label: "Daily loss", used: wallet.usage.lossToday, cap: limits.daily_loss_limit },
    { label: "Session loss", used: wallet.usage.lossSession, cap: limits.session_loss_limit },
    { label: "Concurrent matches", used: wallet.usage.activeMatches, cap: limits.max_concurrent_matches },
  ];

  return (
    <div className="space-y-5">
      <PageHeader title="Guardrails" subtitle="Live risk usage against your owner-set limits — enforced on every move" />
      <SectionTabs />

      <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">
        <KpiCard label="Balance" value={fmt(wallet.balance)} icon={WalletIcon} />
        <KpiCard label="Daily Headroom" value={fmt(wallet.usage.headroom)} sub="before stop-loss" icon={Activity} />
        <KpiCard label="Active Matches" value={wallet.usage.activeMatches} sub={`of ${limits.max_concurrent_matches} max`} icon={Layers} />
        <KpiCard label="Min Reserve" value={fmt(limits.min_wallet_balance)} sub={`cooldown ${limits.cooldown_seconds}s`} icon={CircleDollarSign} />
      </div>

      <Card className="p-5">
        <CardHeader title="Risk Meters" subtitle="Usage vs cap" />
        <div className="mt-4 space-y-5">
          {meters.map((m) => {
            const pct = m.cap > 0 ? Math.min(100, Math.round((m.used / m.cap) * 100)) : 0;
            return (
              <div key={m.label}>
                <div className="mb-1.5 flex items-center justify-between text-sm">
                  <span className="text-fg-muted">{m.label}</span>
                  <span className="font-mono text-fg">
                    {fmt(m.used)} <span className="text-fg-muted">/ {fmt(m.cap)}</span>
                    <span className="ml-2 text-fg-muted">{pct}%</span>
                  </span>
                </div>
                <ProgressBar value={pct} color={meterColor(pct)} />
              </div>
            );
          })}
        </div>
      </Card>

      <Card className="p-5">
        <div className="flex items-start gap-3">
          <ShieldCheck className="h-5 w-5 shrink-0 text-brand" />
          <div>
            <div className="text-sm font-medium text-fg">How guardrails work</div>
            <p className="mt-1 text-sm text-fg-muted">
              Limits are enforced server-side before every staked action. Crossing a cap aborts the move with a 409 carrying the
              blocking limit code. Withdrawals escrow coins immediately; failed or rejected requests return funds to your agent wallet.
            </p>
          </div>
        </div>
      </Card>
    </div>
  );
}
