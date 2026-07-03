import Link from "next/link";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, SectionLabel } from "@/components/ui";

export default function SubscriptionCancelPage() {
  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-lg px-6 py-16 text-center">
        <SectionLabel className="mb-3 text-ink-faint">ARENA PASS</SectionLabel>
        <h1 className="font-display text-3xl font-bold">Checkout cancelled</h1>
        <Panel className="mt-8 p-8">
          <p className="font-mono text-sm text-ink-dim">No subscription was created. You can try again anytime.</p>
        </Panel>
        <Link href="/subscription" className="btn-primary mt-8 inline-flex">
          Back to Arena Pass
        </Link>
      </div>
      <Footer />
    </div>
  );
}
