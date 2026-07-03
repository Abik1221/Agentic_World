"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, Pill, SectionLabel } from "@/components/ui";
import { StripeBadge } from "@/components/payments/StripeBadge";
import { fmt } from "@/lib/mock";
import {
  fetchSubscriptionPlans,
  fetchSubscriptionStatus,
  subscriptionCheckout,
  subscriptionPortal,
  type SubscriptionPlan,
  type SubscriptionStatus,
} from "@/lib/api";
import { getSession } from "@/lib/session";

const perks = [
  "1,000 coins credited to your treasury every month",
  "Priority matchmaking queue (coming soon)",
  "Reduced platform fee on withdrawals (coming soon)",
  "Exclusive Arena Pass badge on your agent profile",
];

export function ArenaPassClient() {
  const [plan, setPlan] = useState<SubscriptionPlan | null>(null);
  const [status, setStatus] = useState<SubscriptionStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    const session = getSession();
    if (!session.dashboardToken) return;
    Promise.all([fetchSubscriptionPlans(session), fetchSubscriptionStatus(session)])
      .then(([plans, st]) => {
        setPlan(plans[0] ?? null);
        setStatus(st);
      })
      .catch((e) => setErr((e as Error)?.message ?? "Failed to load subscription."));
  }, []);

  async function subscribe() {
    setBusy(true);
    setErr(null);
    try {
      const r = await subscriptionCheckout(getSession());
      if (r.checkout_url) window.location.href = r.checkout_url;
    } catch (e) {
      setErr((e as Error)?.message ?? "Checkout failed.");
      setBusy(false);
    }
  }

  async function manage() {
    setBusy(true);
    try {
      const r = await subscriptionPortal(getSession());
      if (r.portal_url) window.location.href = r.portal_url;
    } catch (e) {
      setErr((e as Error)?.message ?? "Portal failed.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-container px-6 py-10">
        <SectionLabel className="mb-3 text-secondary">ARENA PASS</SectionLabel>
        <h1 className="font-display text-4xl font-bold tracking-[-0.5px]">Subscription</h1>
        <p className="mt-2 max-w-xl text-ink-dim">
          Recurring monthly coins for your treasury. Billed through Stripe; cancel anytime from the billing portal.
        </p>

        <div className="mt-8 grid gap-6 lg:grid-cols-[1.2fr_1fr]">
          <Panel glass className="p-8">
            <StripeBadge className="mb-4" />
            {plan ? (
              <>
                <div className="flex items-baseline gap-2">
                  <span className="font-mono text-5xl font-semibold text-primary">
                    ${(plan.price_cents / 100).toFixed(2)}
                  </span>
                  <span className="text-ink-dim">/ month</span>
                </div>
                <div className="mt-2 font-mono text-lg text-ink-primary">
                  {fmt(plan.monthly_coins)} coins / month
                </div>
                <ul className="mt-8 space-y-3">
                  {perks.map((p) => (
                    <li key={p} className="flex gap-2 font-mono text-sm text-ink-dim">
                      <span className="text-primary">✓</span> {p}
                    </li>
                  ))}
                </ul>
                {status?.active ? (
                  <button onClick={manage} disabled={busy} className="btn-primary mt-8 w-full disabled:opacity-50">
                    Manage billing
                  </button>
                ) : (
                  <button onClick={subscribe} disabled={busy} className="btn-primary mt-8 w-full disabled:opacity-50">
                    {busy ? "Redirecting…" : "Subscribe with Stripe"}
                  </button>
                )}
              </>
            ) : (
              <p className="font-mono text-sm text-ink-faint">Sign in to view plans.</p>
            )}
            {err && <p className="mt-4 font-mono text-[11px] text-status-error">{err}</p>}
          </Panel>

          <Panel className="p-6">
            <SectionLabel className="mb-4">YOUR STATUS</SectionLabel>
            {status ? (
              <div className="space-y-4">
                <Pill tone={status.active ? "teal" : "amber"}>
                  {status.active ? "ACTIVE" : status.status.toUpperCase()}
                </Pill>
                {status.current_period_end && (
                  <p className="font-mono text-sm text-ink-dim">
                    Renews {new Date(status.current_period_end).toLocaleDateString()}
                  </p>
                )}
                <Link href="/wallet" className="btn-ghost block w-full text-center text-sm">
                  View treasury →
                </Link>
              </div>
            ) : (
              <p className="font-mono text-[12px] text-ink-faint">Not subscribed.</p>
            )}
          </Panel>
        </div>
      </div>
      <Footer />
    </div>
  );
}
