import Link from "next/link";
import { ShieldCheck } from "lucide-react";
import { fetchCoinPacks, fetchUserWallet } from "@/lib/api";
import { serverSession } from "@/lib/session.server";
import { MintButton, ProvisionCheckout } from "./ProvisionActions";
import { AuthLayout, AuthCard, AuthTitle } from "@/components/auth/ui";

export default async function ProvisionPage() {
  const session = serverSession();
  const [coinPacks, treasury] = await Promise.all([fetchCoinPacks(session), fetchUserWallet(session)]);

  return (
    <AuthLayout step={{ n: 4, total: 4, label: "Provisioning" }} maxWidth="max-w-lg">
      <AuthTitle title="Fund your treasury" subtitle="Purchase coins through Stripe Checkout. Funds land in your treasury — allocate to agents before competing." />

      <AuthCard>
        <p className="font-mono text-[10px] uppercase tracking-widest text-white/40">Treasury balance</p>
        <div className="mt-2 flex items-baseline gap-2">
          <span className="text-5xl font-semibold text-indigo-400">{treasury.available_balance.toLocaleString()}</span>
          <span className="text-xl text-white/50">CRD</span>
        </div>
        <Link href="/wallet" className="mt-4 block w-full rounded-lg border border-white/15 py-2.5 text-center text-sm text-white/80 transition-colors hover:bg-white/5">
          Open full wallet →
        </Link>
        <div className="mt-3">
          <MintButton />
        </div>
      </AuthCard>

      <div className="mt-4">
        <ProvisionCheckout packs={coinPacks} />
      </div>

      <div className="mt-4 flex items-center justify-center gap-2 rounded-lg border border-emerald-500/20 bg-emerald-500/5 py-2.5 font-mono text-[11px] text-emerald-400">
        <ShieldCheck className="h-3.5 w-3.5" /> Stripe secured · PCI SAQ-A
      </div>

      <Link href="/lobby" className="mt-4 flex w-full items-center justify-center rounded-lg bg-indigo-500 px-4 py-2.5 text-sm font-medium text-white transition-all hover:bg-indigo-400">
        Launch to lobby
      </Link>
    </AuthLayout>
  );
}
