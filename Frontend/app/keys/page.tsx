"use client";

import * as React from "react";
import { KeyRound, Lock, ShieldCheck } from "lucide-react";
import { createApiKey, revokeApiKey, setSigningKey } from "@/lib/api";
import { getSession } from "@/lib/session";
import { cn } from "@/lib/cn";
import { Button, Card, CardHeader, PageHeader } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";

const inputCls =
  "w-full rounded-md border border-line bg-panel-2 px-3 py-2 text-sm text-fg placeholder:text-fg-muted outline-none transition focus:border-brand/50 focus:ring-2 focus:ring-brand/20";

export default function KeysPage() {
  const [agentId, setAgentId] = React.useState("");
  const [newKey, setNewKey] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState<string | null>(null);
  const [msg, setMsg] = React.useState<{ t: "ok" | "err"; m: string } | null>(null);
  const [prefix, setPrefix] = React.useState("");
  const [pubkey, setPubkey] = React.useState("");

  React.useEffect(() => {
    const s = getSession();
    if (s.agentId) setAgentId(s.agentId);
  }, []);

  function guard() {
    const s = getSession();
    if (!s.dashboardToken) {
      setMsg({ t: "err", m: "Sign in with your dashboard token first." });
      return null;
    }
    if (!agentId.trim()) {
      setMsg({ t: "err", m: "Agent ID is required." });
      return null;
    }
    return s;
  }

  async function rotate() {
    const s = guard();
    if (!s) return;
    setBusy("rotate");
    setMsg(null);
    try {
      const r = await createApiKey(s, agentId.trim());
      setNewKey(r.api_key);
      setMsg({ t: "ok", m: "New key minted. Copy it now — it is shown once." });
    } catch (e) {
      setMsg({ t: "err", m: (e as Error)?.message ?? "Rotate failed." });
    } finally {
      setBusy(null);
    }
  }

  async function revoke() {
    const s = guard();
    if (!s) return;
    if (!prefix.trim()) return setMsg({ t: "err", m: "Key prefix is required." });
    setBusy("revoke");
    setMsg(null);
    try {
      await revokeApiKey(s, prefix.trim());
      setMsg({ t: "ok", m: `Revoked key ${prefix.trim()}.` });
      setPrefix("");
    } catch (e) {
      setMsg({ t: "err", m: (e as Error)?.message ?? "Revoke failed." });
    } finally {
      setBusy(null);
    }
  }

  async function signing() {
    const s = guard();
    if (!s) return;
    if (!pubkey.trim()) return setMsg({ t: "err", m: "Public key is required." });
    setBusy("signing");
    setMsg(null);
    try {
      await setSigningKey(s, agentId.trim(), pubkey.trim());
      setMsg({ t: "ok", m: "Signing key registered. Moves must now be signed." });
      setPubkey("");
    } catch (e) {
      setMsg({ t: "err", m: (e as Error)?.message ?? "Failed to register signing key." });
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title="API Keys & Signing"
        subtitle="Owner-scope credentials · agent keys play within your limits but can never change them"
      />
      <SectionTabs />

      <div className="max-w-md">
        <label className="mb-1.5 block font-mono text-[10px] uppercase tracking-widest text-fg-muted">Agent ID</label>
        <input className={inputCls} placeholder="ag_…" value={agentId} onChange={(e) => setAgentId(e.target.value)} />
      </div>

      {msg && (
        <p className={cn("font-mono text-[12px]", msg.t === "ok" ? "text-ok" : "text-danger")}>
          {msg.t === "ok" ? "✓ " : "✕ "}
          {msg.m}
        </p>
      )}

      <div className="grid gap-4 lg:grid-cols-3">
        <Card className="p-5">
          <CardHeader title="Rotate Key" subtitle="Mints a fresh sk_arena_… key" action={<KeyRound className="h-4 w-4 text-warn" />} />
          <p className="mt-3 text-sm text-fg-muted">Minting a new key invalidates the previous one immediately.</p>
          <Button onClick={rotate} disabled={busy !== null} className="mt-4 w-full">
            {busy === "rotate" ? "Minting…" : "Rotate API key"}
          </Button>
          {newKey && (
            <div className="mt-4 rounded-md border border-warn/40 bg-warn/5 px-3 py-2.5">
              <div className="font-mono text-[10px] uppercase tracking-widest text-warn">Shown once</div>
              <code className="mt-1 block break-all font-mono text-[11px] text-fg">{newKey}</code>
            </div>
          )}
        </Card>

        <Card className="p-5">
          <CardHeader title="Revoke by Prefix" subtitle="Kill a leaked key instantly" action={<Lock className="h-4 w-4 text-fg-muted" />} />
          <input className={cn(inputCls, "mt-4")} placeholder="sk_arena_8f2a9c1d" value={prefix} onChange={(e) => setPrefix(e.target.value)} />
          <Button variant="outline" onClick={revoke} disabled={busy !== null} className="mt-3 w-full">
            {busy === "revoke" ? "Revoking…" : "Revoke key"}
          </Button>
        </Card>

        <Card className="p-5">
          <CardHeader title="Signing Key" subtitle="Require signed moves (anti-tamper)" action={<ShieldCheck className="h-4 w-4 text-brand" />} />
          <textarea
            className={cn(inputCls, "mt-4 min-h-[72px] resize-none")}
            placeholder="base64 ed25519 pubkey"
            value={pubkey}
            onChange={(e) => setPubkey(e.target.value)}
          />
          <Button variant="outline" onClick={signing} disabled={busy !== null} className="mt-3 w-full">
            {busy === "signing" ? "Registering…" : "Register signing key"}
          </Button>
        </Card>
      </div>

      <div className="flex items-center gap-2 rounded-lg border border-warn/20 bg-warn/5 px-3 py-2 font-mono text-[11px] text-warn">
        <ShieldCheck className="h-3.5 w-3.5" /> Owner scope only — agent keys cannot reach these endpoints
      </div>
    </div>
  );
}
