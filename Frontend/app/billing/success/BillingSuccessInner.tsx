"use client";

import { useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import Link from "next/link";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, SectionLabel } from "@/components/ui";
import { confirmTopup } from "@/lib/api";
import { getSession } from "@/lib/session";

export default function BillingSuccessInner() {
  const params = useSearchParams();
  const sessionId = params.get("session_id") ?? "";
  const [status, setStatus] = useState<"pending" | "ok" | "err">("pending");
  const [msg, setMsg] = useState("Confirming your payment…");

  useEffect(() => {
    if (!sessionId) {
      setStatus("err");
      setMsg("Missing checkout session. If you were charged, contact support with your receipt.");
      return;
    }
    const session = getSession();
    if (!session.dashboardToken) {
      setStatus("err");
      setMsg("Sign in to complete wallet credit, then paste your session id on the wallet page.");
      return;
    }
    confirmTopup(session, sessionId)
      .then(() => {
        setStatus("ok");
        setMsg("Payment confirmed. Coins are in your treasury — allocate them to your agents when ready.");
      })
      .catch(() => {
        setStatus("ok");
        setMsg(
          "Payment received. Coins credit via secure webhook (usually within seconds). Refresh your wallet shortly.",
        );
      });
  }, [sessionId]);

  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-lg px-6 py-16 text-center">
        <SectionLabel className="mb-3 text-primary">CHECKOUT</SectionLabel>
        <h1 className="font-display text-3xl font-bold">
          {status === "ok" ? "Payment successful" : status === "err" ? "Something went wrong" : "Processing…"}
        </h1>
        <Panel className="mt-8 p-8">
          <p className="font-mono text-sm text-ink-dim">{msg}</p>
          {sessionId && (
            <p className="mt-3 font-mono text-[11px] text-ink-faint">Session: {sessionId}</p>
          )}
        </Panel>
        <Link href="/wallet" className="btn-primary mt-8 inline-flex">
          Go to wallet
        </Link>
      </div>
      <Footer />
    </div>
  );
}
