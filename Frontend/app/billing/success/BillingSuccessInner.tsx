"use client";

import { useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { confirmTopup } from "@/lib/api";
import { getSession } from "@/lib/session";
import { StatusScreen } from "@/components/auth/ui";

export default function BillingSuccessInner() {
  const params = useSearchParams();
  const sessionId = params.get("session_id") ?? "";
  const [status, setStatus] = useState<"pending" | "ok" | "error">("pending");
  const [msg, setMsg] = useState("Confirming your payment…");

  useEffect(() => {
    if (!sessionId) {
      setStatus("error");
      setMsg("Missing checkout session. If you were charged, contact support with your receipt.");
      return;
    }
    const session = getSession();
    if (!session.dashboardToken) {
      setStatus("error");
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
        setMsg("Payment received. Coins credit via secure webhook (usually within seconds). Refresh your wallet shortly.");
      });
  }, [sessionId]);

  return (
    <StatusScreen
      tone={status}
      eyebrow="Checkout"
      title={status === "ok" ? "Payment successful" : status === "error" ? "Something went wrong" : "Processing…"}
      message={msg}
      note={sessionId ? `Session: ${sessionId}` : undefined}
      cta={{ label: "Go to wallet", href: "/wallet" }}
    />
  );
}
