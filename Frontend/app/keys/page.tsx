"use client";

import * as React from "react";
import { KeyRound, Lock, ShieldCheck } from "lucide-react";
import { createApiKey, revokeApiKey, setSigningKey, fetchApiKeys, type ApiKeyInfo } from "@/lib/api";
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
  const [keys, setKeys] = React.useState<ApiKeyInfo[] | null>(null);

  const loadKeys = React.useCallback(() => {
    const s = getSession();
    if (!s.dashboardToken) return;
    fetchApiKeys(s)
      .then(setKeys)
      .catch(() => setKeys([]));
  }, []);

  React.useEffect(() => {
    const s = getSession();
    if (s.agentId) setAgentId(s.agentId);
    loadKeys();
  }, [loadKeys]);

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
      setMsg({ t: "ok", m: "New key minted — the previous key is now revoked. Copy this one; it is shown once." });
      loadKeys();
    } catch (e) {
      setMsg({ t: "err", m: (e as Error)?.message ?? "Rotate failed." });
    } finally {
      setBusy(null);
    }
  }

  async function revokeByPrefix(pfx: string) {
    const s = guard();
    if (!s) return;
    if (!pfx) return setMsg({ t: "err", m: "Key prefix is required." });
    setBusy("revoke");
    setMsg(null);
    try {
      await revokeApiKey(s, pfx);
      setMsg({ t: "ok", m: `Revoked key ${pfx}.` });
      setPrefix("");
      loadKeys();
    } catch (e) {
      setMsg({ t: "err", m: (e as Error)?.message ?? "Revoke failed." });
    } finally {
      setBusy(null);
    }
  }
  const revoke = () => revokeByPrefix(prefix.trim());

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

      {/* Key inventory — audit by last-used, revoke by row. */}
      <Card className="p-5">
        <CardHeader title="Your keys" subtitle="One key is active per agent — rotating or `onavion login` revokes the rest" />
        {keys === null ? (
          <p className="mt-3 font-mono text-[12px] text-fg-muted">loading…</p>
        ) : keys.length === 0 ? (
          <p className="mt-3 text-sm text-fg-muted">No keys yet. Rotate above or run <code className="font-mono text-brand">onavion login</code>.</p>
        ) : (
          <div className="mt-3 overflow-x-auto">
            <table className="w-full min-w-[560px] text-sm">
              <thead>
                <tr className="border-b border-line font-mono text-[10px] uppercase tracking-widest text-fg-muted">
                  <th className="py-2 text-left">Key</th>
                  <th className="py-2 text-left">Agent</th>
                  <th className="py-2 text-left">Created</th>
                  <th className="py-2 text-left">Last used</th>
                  <th className="py-2 text-right">Status</th>
                </tr>
              </thead>
              <tbody>
                {keys.map((k) => {
                  const revoked = Boolean(k.revoked_at);
                  return (
                    <tr key={k.prefix} className={cn("border-b border-line/50", revoked && "opacity-50")}>
                      <td className="py-2.5 font-mono text-[12px] text-fg">{k.prefix}…</td>
                      <td className="py-2.5 font-mono text-[11px] text-fg-muted">{k.agent}</td>
                      <td className="py-2.5 font-mono text-[11px] text-fg-muted">{fmtDate(k.created_at)}</td>
                      <td className="py-2.5 font-mono text-[11px] text-fg-muted">{k.last_used_at ? fmtDate(k.last_used_at) : "never"}</td>
                      <td className="py-2.5 text-right">
                        {revoked ? (
                          <span className="font-mono text-[11px] text-fg-muted">revoked</span>
                        ) : (
                          <button
                            onClick={() => revokeByPrefix(k.prefix)}
                            disabled={busy !== null}
                            className="font-mono text-[11px] text-danger hover:underline disabled:opacity-50"
                          >
                            Revoke
                          </button>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <div className="flex items-center gap-2 rounded-lg border border-warn/20 bg-warn/5 px-3 py-2 font-mono text-[11px] text-warn">
        <ShieldCheck className="h-3.5 w-3.5" /> Owner scope only — agent keys cannot reach these endpoints
      </div>
    </div>
  );
}

function fmtDate(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  return new Date(t).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
}
