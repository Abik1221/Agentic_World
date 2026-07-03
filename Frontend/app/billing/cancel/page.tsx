import Link from "next/link";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, SectionLabel } from "@/components/ui";

export default function BillingCancelPage() {
  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-lg px-6 py-16 text-center">
        <SectionLabel className="mb-3 text-ink-faint">CHECKOUT</SectionLabel>
        <h1 className="font-display text-3xl font-bold">Payment cancelled</h1>
        <Panel className="mt-8 p-8">
          <p className="font-mono text-sm text-ink-dim">
            No charge was made. You can return to the wallet anytime to purchase coin packs.
          </p>
        </Panel>
        <Link href="/wallet" className="btn-primary mt-8 inline-flex">
          Back to wallet
        </Link>
      </div>
      <Footer />
    </div>
  );
}
