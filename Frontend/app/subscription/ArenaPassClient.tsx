"use client";

import * as React from "react";
import { useEffect, useState } from "react";
import Link from "next/link";
import { Check, Star } from "lucide-react";
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
import { Badge, Button, Card, CardHeader, PageHeader } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";

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
    <div className="space-y-5">
      <PageHeader title="Arena Pass" subtitle="Recurring monthly coins for your treasury — billed via Stripe, cancel anytime" />
      <SectionTabs />

      <div className="grid gap-4 lg:grid-cols-[1.2fr_1fr]">
        <Card className="p-6">
          <StripeBadge className="mb-4" />
          {plan ? (
            <>
              <div className="flex items-baseline gap-2">
                <span className="text-5xl font-semibold text-brand">${(plan.price_cents / 100).toFixed(2)}</span>
                <span className="text-fg-muted">/ month</span>
              </div>
              <div className="mt-2 font-mono text-lg text-fg">{fmt(plan.monthly_coins)} coins / month</div>
              <ul className="mt-6 space-y-2.5">
                {perks.map((p) => (
                  <li key={p} className="flex items-start gap-2 text-sm text-fg-muted">
                    <Check className="mt-0.5 h-4 w-4 shrink-0 text-brand" /> {p}
                  </li>
                ))}
              </ul>
              {status?.active ? (
                <Button onClick={manage} disabled={busy} className="mt-6 w-full">
                  Manage billing
                </Button>
              ) : (
                <Button onClick={subscribe} disabled={busy} className="mt-6 w-full">
                  {busy ? "Redirecting…" : "Subscribe with Stripe"}
                </Button>
              )}
            </>
          ) : (
            <p className="font-mono text-sm text-fg-muted">Sign in to view plans.</p>
          )}
          {err && <p className="mt-4 font-mono text-[11px] text-danger">{err}</p>}
        </Card>

        <Card className="p-5">
          <CardHeader title="Your Status" action={<Star className="h-4 w-4 text-warn" />} />
          {status ? (
            <div className="mt-4 space-y-4">
              <Badge tone={status.active ? "ok" : "warn"}>{status.active ? "Active" : status.status}</Badge>
              {status.current_period_end && (
                <p className="font-mono text-sm text-fg-muted">Renews {new Date(status.current_period_end).toLocaleDateString()}</p>
              )}
              <Button asChild variant="outline" size="sm" className="w-full">
                <Link href="/wallet">View treasury →</Link>
              </Button>
            </div>
          ) : (
            <p className="mt-4 font-mono text-[12px] text-fg-muted">Not subscribed.</p>
          )}
        </Card>
      </div>
    </div>
  );
}
