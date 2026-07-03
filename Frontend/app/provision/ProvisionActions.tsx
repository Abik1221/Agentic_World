"use client";

import { useState } from "react";
import { Bolt } from "@/components/icons";
import { CoinPackCheckout } from "@/components/payments/CoinPackCheckout";
import { fmt, type CoinPack } from "@/lib/mock";
import { mintCoins } from "@/lib/api";
import { getSession } from "@/lib/session";

export function MintButton() {
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  async function mint() {
    const session = getSession();
    if (!session.agentId) {
      setMsg("Connect a session first.");
      return;
    }
    setBusy(true);
    setMsg(null);
    try {
      await mintCoins(session, session.agentId, 1000);
      setMsg("✓ Minted 1,000 test coins. Refresh wallet.");
    } catch (e) {
      setMsg("✕ " + ((e as Error)?.message ?? "Mint failed."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <button
        type="button"
        onClick={mint}
        disabled={busy}
        className="btn-primary mt-5 w-full disabled:cursor-not-allowed disabled:opacity-50"
      >
        <Bolt width={15} height={15} /> {busy ? "Minting…" : "Mint_Test_Coins"}
      </button>
      <p className="mt-2 text-center font-mono text-[11px] text-ink-faint">
        {msg ?? `DEV ONLY · ${fmt(1000)} CRD`}
      </p>
    </>
  );
}

export function ProvisionCheckout({ packs }: { packs: CoinPack[] }) {
  return <CoinPackCheckout packs={packs} compact />;
}
