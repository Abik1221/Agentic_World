"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { confirmSubscription } from "@/lib/api";
import { getSession } from "@/lib/session";
import { StatusScreen } from "@/components/auth/ui";

function Inner() {
  const params = useSearchParams();
  const sessionId = params.get("session_id") ?? "";
  const [msg, setMsg] = useState("Activating Arena Pass…");
  const [tone, setTone] = useState<"ok" | "pending">("pending");

  useEffect(() => {
    if (!sessionId) {
      setMsg("Missing session. Check your email receipt or contact support.");
      return;
    }
    confirmSubscription(getSession(), sessionId)
      .then(() => {
        setTone("ok");
        setMsg("Arena Pass is active. Your monthly coins will credit to your treasury.");
      })
      .catch(() => {
        setTone("ok");
        setMsg("Subscription received. Stripe webhooks will activate your pass within a few seconds.");
      });
  }, [sessionId]);

  return <StatusScreen tone={tone} eyebrow="Arena Pass" title="Welcome aboard" message={msg} cta={{ label: "Go to wallet", href: "/wallet" }} />;
}

export default function SubscriptionSuccessPage() {
  return (
    <Suspense fallback={null}>
      <Inner />
    </Suspense>
  );
}
