import Link from "next/link";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, SectionLabel } from "@/components/ui";

export default function PayoutsReturnPage() {
  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-lg px-6 py-16 text-center">
        <SectionLabel className="mb-3 text-primary">STRIPE CONNECT</SectionLabel>
        <h1 className="font-display text-3xl font-bold">Payout setup complete</h1>
        <Panel className="mt-8 p-8">
          <p className="font-mono text-sm text-ink-dim">
            Your identity verification is on file. You can now request withdrawals of net winnings
            from the Guardrails page after the clearing window.
          </p>
        </Panel>
        <Link href="/guardrails" className="btn-primary mt-8 inline-flex">
          Go to cash out
        </Link>
      </div>
      <Footer />
    </div>
  );
}
