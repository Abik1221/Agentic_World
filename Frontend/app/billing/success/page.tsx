"use client";

import { Suspense } from "react";
import BillingSuccessInner from "./BillingSuccessInner";

export default function BillingSuccessPage() {
  return (
    <Suspense fallback={<p className="p-10 text-center font-mono text-sm">Loading…</p>}>
      <BillingSuccessInner />
    </Suspense>
  );
}
