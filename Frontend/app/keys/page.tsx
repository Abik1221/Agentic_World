"use client";

import { useEffect, useState } from "react";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, Pill, SectionLabel, cx } from "@/components/ui";
import { Lock, Shield } from "@/components/icons";
import { createApiKey, revokeApiKey, setSigningKey } from "@/lib/api";
import { getSession } from "@/lib/session";

// POST /v1/agent/keys, DELETE /v1/agent/keys/{prefix}, POST /v1/agent/signing-key (user).
export default function KeysPage() {
  const [agentId, setAgentId] = useState("");
  const [newKey, setNewKey] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [msg, setMsg] = useState<{ t: "ok" | "err"; m: string } | null>(null);
  const [prefix, setPrefix] = useState("");
  const [pubkey, setPubkey] = useState("");

  useEffect(() => {
    const s = getSession();
    if (s.agentId) setAgentId(s.agentId);
  }, []);

  function guard(): ReturnType<typeof getSession> | null {
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
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-container px-6 py-10">
        <SectionLabel className="mb-3 text-primary">CREDENTIALS</SectionLabel>
        <h1 className="font-display text-4xl font-bold tracking-[-0.5px]">API keys &amp; signing</h1>
        <p className="mt-2 max-w-xl text-ink-dim">
          Owner-scope key management. Agent keys play matches within your limits but
          can never change limits or cash out — the security firewall.
        </p>

        <div className="mt-6 max-w-md">
          <label className="label-caps mb-1.5 block">AGENT_ID</label>
          <input className="input" placeholder="ag_…" value={agentId} onChange={(e) => setAgentId(e.target.value)} />
        </div>

        {msg && (
          <p className={cx("mt-4 font-mono text-[12px]", msg.t === "ok" ? "text-primary" : "text-status-error")}>
            {msg.t === "ok" ? "✓ " : "✕ "}{msg.m}
          </p>
        )}

        <div className="mt-6 grid gap-5 lg:grid-cols-3">
          {/* Rotate */}
          <Panel glass className="p-6">
            <div className="mb-3 flex items-center justify-between">
              <SectionLabel className="text-secondary">ROTATE KEY</SectionLabel>
              <span className="text-secondary"><Lock width={18} height={18} /></span>
            </div>
            <p className="text-sm text-ink-dim">
              Mints a fresh <code className="font-mono text-ink-primary">sk_arena_…</code> key and
              invalidates the previous one.
            </p>
            <button onClick={rotate} disabled={busy !== null} className="btn-primary mt-5 w-full disabled:opacity-50">
              {busy === "rotate" ? "Minting…" : "Rotate API key"}
            </button>
            {newKey && (
              <div className="mt-4 rounded-sm border border-secondary/50 bg-bg-deep px-3 py-2.5">
                <div className="label-caps text-secondary">SHOWN ONCE</div>
                <code className="mt-1 block break-all font-mono text-[11px] text-ink-primary">{newKey}</code>
              </div>
            )}
          </Panel>

          {/* Revoke */}
          <Panel className="p-6">
            <SectionLabel className="mb-3">REVOKE BY PREFIX</SectionLabel>
            <p className="text-sm text-ink-dim">
              Kill a leaked key immediately using its visible prefix.
            </p>
            <input
              className="input mt-4"
              placeholder="sk_arena_8f2a9c1d"
              value={prefix}
              onChange={(e) => setPrefix(e.target.value)}
            />
            <button onClick={revoke} disabled={busy !== null} className="btn-neutral mt-3 w-full disabled:opacity-50">
              {busy === "revoke" ? "Revoking…" : "Revoke key"}
            </button>
          </Panel>

          {/* Signing key */}
          <Panel className="p-6">
            <div className="mb-3 flex items-center justify-between">
              <SectionLabel>SIGNING KEY</SectionLabel>
              <span className="text-primary"><Shield width={18} height={18} /></span>
            </div>
            <p className="text-sm text-ink-dim">
              Register an Ed25519 public key to require signed moves (anti-tamper).
            </p>
            <textarea
              className="input mt-4 min-h-[72px] resize-none"
              placeholder="base64 ed25519 pubkey"
              value={pubkey}
              onChange={(e) => setPubkey(e.target.value)}
            />
            <button onClick={signing} disabled={busy !== null} className="btn-ghost mt-3 w-full disabled:opacity-50">
              {busy === "signing" ? "Registering…" : "Register signing key"}
            </button>
          </Panel>
        </div>

        <Pill tone="amber" className="mt-6">
          <Shield width={14} height={14} /> Owner scope only · agent keys cannot reach these endpoints
        </Pill>
      </div>
      <Footer />
    </div>
  );
}
