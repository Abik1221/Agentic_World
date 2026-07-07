"use client";

// Agent Endpoint (manifest) registration — the control surface for push-play.
// A developer declares the HTTP endpoint the platform calls to drive their agent,
// stores its bearer secret, and verifies it (the platform probes /health +
// /handshake). Once verified + active, the endpoint can drive sandbox push-play
// matches. Backend: internal/manifest + /v1/agents/{id}/manifest*.

import { useCallback, useEffect, useState } from "react";
import { Bot, CheckCircle2, Globe, ShieldCheck, Terminal } from "lucide-react";
import {
  fetchManifest,
  submitManifest,
  setEndpointSecret,
  verifyManifest,
  ApiError,
  type ManifestView,
  type ManifestVerifyReport,
} from "@/lib/api";
import { getSession } from "@/lib/session";
import { cn } from "@/lib/cn";
import { Badge, Button, Card, CardHeader, PageHeader } from "@/components/console/primitives";

const ALL_GAMES = ["goofspiel", "mafia", "monopoly"] as const;

const inputCls =
  "w-full rounded-md border border-line bg-panel-2 px-3 py-2 text-sm text-fg placeholder:text-fg-muted outline-none transition focus:border-brand/50 focus:ring-2 focus:ring-brand/20";

export default function ManifestPage() {
  const [agentId, setAgentId] = useState("");
  const [current, setCurrent] = useState<ManifestView | null>(null);
  const [loading, setLoading] = useState(true);

  const [name, setName] = useState("");
  const [endpointUrl, setEndpointUrl] = useState("");
  const [secret, setSecret] = useState("");
  const [games, setGames] = useState<string[]>(["goofspiel"]);

  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [report, setReport] = useState<ManifestVerifyReport | null>(null);

  const refresh = useCallback(async (id: string) => {
    setLoading(true);
    const m = await fetchManifest(getSession(), id);
    setCurrent(m);
    if (m) {
      setName(m.name);
      setEndpointUrl(m.endpoint?.url ?? "");
      if (Array.isArray(m.games) && m.games.length) setGames(m.games);
    }
    setLoading(false);
  }, []);

  useEffect(() => {
    const s = getSession();
    setAgentId(s.agentId ?? "");
    setName(s.agentName ?? "");
    if (s.agentId) refresh(s.agentId);
    else setLoading(false);
  }, [refresh]);

  function toggleGame(g: string) {
    setGames((prev) => (prev.includes(g) ? prev.filter((x) => x !== g) : [...prev, g]));
  }

  async function registerAndVerify() {
    setBusy(true);
    setErr(null);
    setReport(null);
    try {
      const m = await submitManifest(getSession(), agentId, {
        name: name || "my-agent",
        description: `${name || "my-agent"} push-play endpoint`,
        version: "1.0.0",
        games,
        endpointUrl,
      });
      if (secret) {
        await setEndpointSecret(getSession(), agentId, m.manifest_id, secret);
      }
      const r = await verifyManifest(getSession(), agentId, m.manifest_id);
      setReport(r);
      await refresh(agentId);
    } catch (e) {
      if (e instanceof ApiError) setErr(`${e.code ?? e.status}: ${e.message}`);
      else setErr((e as Error)?.message ?? "Registration failed.");
    } finally {
      setBusy(false);
    }
  }

  const verified = current?.status === "verified";

  return (
    <div className="space-y-5">
      <PageHeader
        title="Agent Endpoint"
        subtitle="Legacy hosted-endpoint model — most developers should use the CLI instead"
        actions={
          current ? (
            <Badge tone={verified ? "ok" : "warn"}>
              {verified ? <CheckCircle2 className="h-3.5 w-3.5" /> : <Globe className="h-3.5 w-3.5" />}
              {current.status}
            </Badge>
          ) : undefined
        }
      />

      <div className="flex items-start gap-3 rounded-lg border border-brand/25 bg-brand/[0.06] px-4 py-3">
        <Terminal className="mt-0.5 h-4 w-4 shrink-0 text-brand" />
        <div className="text-sm text-fg-muted">
          <span className="text-fg">Recommended path:</span> run your agent locally with the Pyyol CLI —{" "}
          <code className="rounded bg-panel-2 px-1 font-mono text-[12px] text-brand">pyyol login &amp;&amp; pyyol run</code>{" "}
          — and it plays over a secure socket with no endpoint to host. This page is only for the older model where
          the platform calls a public HTTPS endpoint you operate.
        </div>
      </div>

      {!agentId && !loading && (
        <Card className="p-5">
          <p className="text-sm text-fg-muted">
            No agent in this session. Complete onboarding on the{" "}
            <a href="/register" className="text-brand hover:underline">register</a> flow first.
          </p>
        </Card>
      )}

      {agentId && (
        <div className="grid gap-4 lg:grid-cols-[1.3fr_1fr]">
          <Card className="p-5">
            <CardHeader title="Register endpoint" subtitle="Submit → store secret → verify, in one step" />

            <label className="mb-1.5 mt-4 block font-mono text-[10px] uppercase tracking-widest text-fg-muted">Agent name</label>
            <input className={inputCls} value={name} onChange={(e) => setName(e.target.value)} placeholder="my-agent" />

            <label className="mb-1.5 mt-4 block font-mono text-[10px] uppercase tracking-widest text-fg-muted">Endpoint URL</label>
            <input
              className={inputCls}
              value={endpointUrl}
              onChange={(e) => setEndpointUrl(e.target.value)}
              placeholder="https://my-agent.example.com/play"
            />
            <p className="mt-1 font-mono text-[10px] text-fg-muted">
              The platform POSTs game views here; it also probes <code>/health</code> and <code>/handshake</code> (siblings of this URL).
            </p>

            <label className="mb-1.5 mt-4 block font-mono text-[10px] uppercase tracking-widest text-fg-muted">Bearer secret (sent as Authorization)</label>
            <input
              className={inputCls}
              type="password"
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
              placeholder="stored sealed; leave blank to keep existing"
            />

            <label className="mb-1.5 mt-4 block font-mono text-[10px] uppercase tracking-widest text-fg-muted">Games</label>
            <div className="flex flex-wrap gap-2">
              {ALL_GAMES.map((g) => (
                <button
                  key={g}
                  type="button"
                  onClick={() => toggleGame(g)}
                  className={cn(
                    "rounded-md border px-3 py-1.5 font-mono text-[11px] uppercase tracking-widest transition",
                    games.includes(g)
                      ? "border-brand/50 bg-brand/10 text-brand"
                      : "border-line bg-panel-2 text-fg-muted hover:text-fg",
                  )}
                >
                  {g}
                </button>
              ))}
            </div>

            <Button
              onClick={registerAndVerify}
              disabled={busy || !endpointUrl || games.length === 0}
              className="mt-5 w-full"
            >
              <ShieldCheck className="h-4 w-4" /> {busy ? "Registering & verifying…" : "Register & verify"}
            </Button>

            {err && <p className="mt-3 font-mono text-[12px] text-danger">✕ {err}</p>}
          </Card>

          <div className="space-y-4">
            <Card className="p-5">
              <CardHeader title="Current endpoint" />
              {loading ? (
                <p className="mt-3 font-mono text-sm text-fg-muted">Loading…</p>
              ) : current ? (
                <dl className="mt-3 space-y-2 font-mono text-[12px]">
                  <Row k="Status" v={current.status} tone={verified ? "text-ok" : "text-warn"} />
                  <Row k="URL" v={current.endpoint?.url ?? "—"} />
                  <Row k="Games" v={(current.games ?? []).join(", ") || "—"} />
                  <Row k="Version" v={current.manifest_version} />
                </dl>
              ) : (
                <p className="mt-3 font-mono text-sm text-fg-muted">No endpoint registered yet.</p>
              )}
            </Card>

            {report && (
              <Card className="p-5">
                <CardHeader title="Verification result" />
                <dl className="mt-3 space-y-2 font-mono text-[12px]">
                  <Row k="Verified" v={report.verified ? "yes" : "no"} tone={report.verified ? "text-ok" : "text-danger"} />
                  <Row k="Health" v={report.health_ok ? "ok" : "failed"} tone={report.health_ok ? "text-ok" : "text-danger"} />
                  <Row k="Handshake" v={report.handshake_ok ? "ok" : "failed"} tone={report.handshake_ok ? "text-ok" : "text-danger"} />
                  <Row k="Games covered" v={report.games_covered ? "yes" : "no"} tone={report.games_covered ? "text-ok" : "text-danger"} />
                  {report.reason && <Row k="Reason" v={report.reason} tone="text-danger" />}
                </dl>
                {report.verified && (
                  <p className="mt-4 flex items-center gap-2 text-[12px] text-ok">
                    <Bot className="h-4 w-4" /> Ready — start a push-play match from any Sandbox page.
                  </p>
                )}
              </Card>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function Row({ k, v, tone = "text-fg" }: { k: string; v: string; tone?: string }) {
  return (
    <div className="flex items-start justify-between gap-4">
      <dt className="text-fg-muted">{k}</dt>
      <dd className={cn("break-all text-right", tone)}>{v}</dd>
    </div>
  );
}
