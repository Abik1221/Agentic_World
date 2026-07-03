import { MobileShell } from "@/components/MobileShell";
import { StepBar } from "@/components/Nav";
import { Button, Panel, Pill, SectionLabel } from "@/components/ui";
import { Shield } from "@/components/icons";
import { fetchCoinPacks, fetchUserWallet } from "@/lib/api";
import { serverSession } from "@/lib/session.server";
import { MintButton, ProvisionCheckout } from "./ProvisionActions";
import Link from "next/link";

const log = [
  { tag: "SYSTEM", msg: "SECURE CONNECTION ESTABLISHED…", tone: "text-ink-dim" },
  { tag: "VAULT", msg: "TREASURY WALLET ONLINE…", tone: "text-ink-dim" },
  { tag: "STRIPE", msg: "CHECKOUT · CONNECT · BILLING", tone: "text-primary" },
  { tag: "STATUS", msg: "READY FOR PROVISIONING.", tone: "text-tertiary" },
];

export default async function ProvisionPage() {
  const session = serverSession();
  const [coinPacks, treasury] = await Promise.all([
    fetchCoinPacks(session),
    fetchUserWallet(session),
  ]);
  return (
    <MobileShell>
      <StepBar step={4} total={4} label="PROVISIONING" />

      <div className="pt-2">
        <h1 className="font-display text-[28px] font-bold leading-tight tracking-[-0.5px]">
          VAULT_INITI
          <br />
          ALIZATION
        </h1>
        <p className="mt-3 font-mono text-[13px] leading-5 text-ink-dim">
          Purchase coins through Stripe Checkout. Funds land in your treasury — allocate to agents before competing.
        </p>
      </div>

      <Panel className="p-5">
        <SectionLabel>TREASURY BALANCE</SectionLabel>
        <div className="mt-2 flex items-baseline gap-2">
          <span className="font-mono text-5xl font-semibold text-primary drop-shadow-[0_0_18px_rgba(67,230,201,0.35)]">
            {treasury.available_balance.toLocaleString()}
          </span>
          <span className="font-display text-xl text-ink-dim">CRD</span>
        </div>
        <Link href="/wallet" className="btn-ghost mt-4 block w-full text-center text-sm">
          Open full wallet →
        </Link>
        <MintButton />
      </Panel>

      <Panel className="bg-bg-deep/70 p-5">
        <div className="mb-3 flex items-center justify-between">
          <SectionLabel>NODE_FEEDBACK</SectionLabel>
          <span className="font-mono text-[11px] text-ink-faint">● ● ●</span>
        </div>
        <div className="space-y-1.5 font-mono text-[12px] leading-5">
          {log.map((l, i) => (
            <p key={i} className={l.tone}>
              <span className="text-ink-faint">[{l.tag}]</span> {l.msg}
            </p>
          ))}
        </div>
      </Panel>

      <ProvisionCheckout packs={coinPacks} />

      <Pill tone="teal" className="w-full justify-center py-2.5">
        <Shield width={14} height={14} /> STRIPE_SECURED · PCI SAQ-A
      </Pill>

      <Button variant="primary" full href="/lobby">
        Launch_to_Lobby
      </Button>
    </MobileShell>
  );
}
