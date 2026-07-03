import Link from "next/link";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, SectionLabel } from "@/components/ui";

export default function PayoutsRefreshPage() {
  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-lg px-6 py-16 text-center">
        <SectionLabel className="mb-3 text-secondary">STRIPE CONNECT</SectionLabel>
        <h1 className="font-display text-3xl font-bold">Continue setup</h1>
        <Panel className="mt-8 p-8">
          <p className="font-mono text-sm text-ink-dim">
            Your onboarding link expired. Start Stripe Connect again from Guardrails to finish KYC.
          </p>
        </Panel>
        <Link href="/guardrails" className="btn-primary mt-8 inline-flex">
          Resume onboarding
        </Link>
      </div>
      <Footer />
    </div>
  );
}
