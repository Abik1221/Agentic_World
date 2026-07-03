"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import Link from "next/link";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, SectionLabel } from "@/components/ui";
import { confirmSubscription } from "@/lib/api";
import { getSession } from "@/lib/session";

function Inner() {
  const params = useSearchParams();
  const sessionId = params.get("session_id") ?? "";
  const [msg, setMsg] = useState("Activating Arena Pass…");

  useEffect(() => {
    if (!sessionId) {
      setMsg("Missing session. Check your email receipt or contact support.");
      return;
    }
    confirmSubscription(getSession(), sessionId)
      .then(() => setMsg("Arena Pass is active. Your monthly coins will credit to your treasury."))
      .catch(() =>
        setMsg("Subscription received. Stripe webhooks will activate your pass within a few seconds."),
      );
  }, [sessionId]);

  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-lg px-6 py-16 text-center">
        <SectionLabel className="mb-3 text-secondary">ARENA PASS</SectionLabel>
        <h1 className="font-display text-3xl font-bold">Welcome aboard</h1>
        <Panel className="mt-8 p-8">
          <p className="font-mono text-sm text-ink-dim">{msg}</p>
        </Panel>
        <Link href="/wallet" className="btn-primary mt-8 inline-flex">
          Go to wallet
        </Link>
      </div>
      <Footer />
    </div>
  );
}

export default function SubscriptionSuccessPage() {
  return (
    <Suspense fallback={<p className="p-10 text-center">Loading…</p>}>
      <Inner />
    </Suspense>
  );
}
