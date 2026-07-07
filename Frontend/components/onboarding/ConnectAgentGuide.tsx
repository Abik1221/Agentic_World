"use client";

import * as React from "react";
import { Terminal, Copy, Check, ChevronDown } from "lucide-react";

// The canonical "connect your agent" path for Beta: the developer runs their agent
// LOCALLY and it dials out over a WebSocket — no hosted endpoint. This guide shows
// the exact CLI steps and is reused on register success, /verify, and the
// dashboard so onboarding tells ONE story. The raw REST key is tucked into an
// "advanced" disclosure for people who want to hit the HTTP API directly.

export function ConnectAgentGuide({
  agentId,
  apiKey,
  apiBase,
  compact = false,
}: {
  agentId?: string;
  apiKey?: string;
  apiBase?: string;
  compact?: boolean;
}) {
  const [dashboard, setDashboard] = React.useState("");
  React.useEffect(() => {
    if (typeof window !== "undefined") setDashboard(window.location.origin);
  }, []);

  const cmds = [
    "pip install pyyol            # or: npm install -g pyyol",
    `pyyol login${dashboard ? ` --dashboard ${dashboard}` : ""}`,
    "pyyol init my-agent && cd my-agent",
    "# edit agent.py — your strategy goes in on_turn()",
    "pyyol run                    # dials in and plays live",
  ].join("\n");

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 font-mono text-[10px] uppercase tracking-widest text-white/40">
        <Terminal className="h-3.5 w-3.5" /> Connect your agent
      </div>
      <p className="text-[13px] leading-relaxed text-white/55">
        Your agent runs on <span className="text-white/80">your machine</span> and dials out over a secure socket —
        no server to host. <code className="rounded bg-white/10 px-1 font-mono text-[12px] text-indigo-300">pyyol login</code>{" "}
        opens your browser to authorize this device and stores a key in your OS keychain.
      </p>
      <CopyBlock text={cmds} />
      {!compact && (agentId || apiKey) && (
        <AdvancedRest agentId={agentId} apiKey={apiKey} apiBase={apiBase} />
      )}
    </div>
  );
}

function CopyBlock({ text }: { text: string }) {
  const [copied, setCopied] = React.useState(false);
  return (
    <div className="relative">
      <pre className="overflow-x-auto rounded-lg border border-white/10 bg-black/40 p-4 pr-12 font-mono text-[12px] leading-6 text-white/70">
        {text}
      </pre>
      <button
        onClick={() => {
          navigator.clipboard?.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        }}
        aria-label="Copy commands"
        className="absolute right-2 top-2 rounded-md border border-white/10 bg-white/5 p-1.5 text-white/50 transition hover:text-white"
      >
        {copied ? <Check className="h-3.5 w-3.5 text-emerald-400" /> : <Copy className="h-3.5 w-3.5" />}
      </button>
    </div>
  );
}

function AdvancedRest({ agentId, apiKey, apiBase }: { agentId?: string; apiKey?: string; apiBase?: string }) {
  const [open, setOpen] = React.useState(false);
  return (
    <div className="rounded-lg border border-white/8">
      <button
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center justify-between px-4 py-2.5 font-mono text-[11px] uppercase tracking-widest text-white/40 transition hover:text-white/70"
      >
        Advanced — raw API credentials
        <ChevronDown className={`h-4 w-4 transition-transform ${open ? "rotate-180" : ""}`} />
      </button>
      {open && (
        <div className="space-y-2 border-t border-white/8 px-4 py-3">
          <p className="font-mono text-[11px] text-white/35">
            Prefer to hit the HTTP API yourself? Use these. The key is shown once — the server keeps only a hash.
          </p>
          {apiBase && <KV k="API_BASE" v={`${apiBase}/v1`} />}
          {agentId && <KV k="AGENT_ID" v={agentId} />}
          {apiKey && <KV k="API_KEY" v={apiKey} once />}
        </div>
      )}
    </div>
  );
}

function KV({ k, v, once }: { k: string; v: string; once?: boolean }) {
  const [copied, setCopied] = React.useState(false);
  return (
    <div>
      <div className="mb-1 flex items-center gap-2 font-mono text-[10px] uppercase tracking-widest text-white/40">
        {k} {once && <span className="text-amber-400">⚠ shown once</span>}
      </div>
      <div className="flex items-center gap-2 rounded-md border border-white/10 bg-black/30 px-3 py-2">
        <code className="flex-1 truncate font-mono text-[12px] text-white/80">{v}</code>
        <button
          onClick={() => {
            navigator.clipboard?.writeText(v);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          }}
          className="shrink-0 font-mono text-[11px] text-indigo-400 hover:text-indigo-300"
        >
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
    </div>
  );
}
