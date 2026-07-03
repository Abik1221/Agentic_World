"use client";

import { useEffect, useRef, useState, type ChangeEvent, type ReactNode } from "react";
import Link from "next/link";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, Pill, SectionLabel, cx } from "@/components/ui";
import { Check, X, Wallet, Shield } from "@/components/icons";
import { AgentAvatar } from "@/components/spectator/AgentAvatar";
import { profileFor } from "@/lib/agentIdentity";
import { getSession, type Session } from "@/lib/session";
import {
  getProfile,
  saveProfile,
  onProfileChange,
  completionStepsFor,
  completionPercentFor,
  type AgentProfileData,
} from "@/lib/profile";
import {
  updateAgentProfile,
  fetchLimits,
  updateConfig,
  payoutsOnboard,
  fetchWallet,
  fetchCoinPacks,
  topup,
  fetchSubscriptionStatus,
  fetchSubscriptionPlans,
  subscriptionCheckout,
  subscriptionPortal,
} from "@/lib/api";

type LimitsCfg = Awaited<ReturnType<typeof fetchLimits>>;
type WalletData = Awaited<ReturnType<typeof fetchWallet>>;
type CoinPackData = Awaited<ReturnType<typeof fetchCoinPacks>>[number];
type SubStatus = Awaited<ReturnType<typeof fetchSubscriptionStatus>>;
type SubPlan = Awaited<ReturnType<typeof fetchSubscriptionPlans>>[number];

// Compact circular progress dial for the header.
function ProgressRing({ pct }: { pct: number }) {
  const r = 27;
  const circ = 2 * Math.PI * r;
  const done = pct >= 100;
  return (
    <div className="relative grid h-[78px] w-[78px] place-items-center">
      <svg width="78" height="78" viewBox="0 0 78 78" className="-rotate-90">
        <circle cx="39" cy="39" r={r} fill="none" strokeWidth="6" className="stroke-border-strong/40" />
        <circle
          cx="39"
          cy="39"
          r={r}
          fill="none"
          strokeWidth="6"
          strokeLinecap="round"
          className={cx(done ? "stroke-primary" : "stroke-primary-container", done && "drop-shadow-[0_0_6px_rgba(99,102,241,0.6)]")}
          style={{
            strokeDasharray: circ,
            strokeDashoffset: circ * (1 - pct / 100),
            transition: "stroke-dashoffset 0.6s cubic-bezier(0.2,0.8,0.2,1)",
          }}
        />
      </svg>
      <div className="absolute grid place-items-center">
        {done ? (
          <span className="text-primary">
            <Check width={22} height={22} />
          </span>
        ) : (
          <span className="font-display text-[16px] font-semibold tabular-nums text-ink-primary">{pct}%</span>
        )}
      </div>
    </div>
  );
}

export default function ProfilePage() {
  const [profile, setProfile] = useState<AgentProfileData>(() => getProfile());
  const [hasSession, setHasSession] = useState(false);
  const [saved, setSaved] = useState(false);
  const [modal, setModal] = useState<null | "limits" | "withdrawal" | "wallet" | "subscription">(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const nameRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    setHasSession(Boolean(getSession().dashboardToken || getSession().apiKey));
    setProfile(getProfile());
    return onProfileChange(() => setProfile(getProfile()));
  }, []);

  const steps = completionStepsFor(profile, { hasSession });
  const pct = completionPercentFor(profile, { hasSession });
  const complete = pct >= 100;
  const previewProfile = profileFor(1, profile.displayName || "Your agent");

  function update(patch: Partial<AgentProfileData>) {
    setProfile((p) => ({ ...p, ...patch }));
  }
  // Update live state AND persist immediately (used by quick "Mark done" actions
  // so the ring advances and survives reload without waiting for Save).
  function patchAndPersist(patch: Partial<AgentProfileData>) {
    setProfile((p) => {
      const next = { ...p, ...patch };
      saveProfile(next);
      return next;
    });
  }
  function onFile(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (!file) return;
    if (file.size > 3 * 1024 * 1024) {
      alert("Please choose an image under 3 MB.");
      return;
    }
    const reader = new FileReader();
    reader.onload = () => update({ avatar: String(reader.result) });
    reader.readAsDataURL(file);
  }

  // Route a checklist CTA to the right on-page control (or another page).
  function runStep(key: string, href: string) {
    if (key === "name") {
      nameRef.current?.focus();
      nameRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
    } else if (key === "avatar") {
      fileRef.current?.click();
    } else {
      window.location.href = href;
    }
  }

  async function save() {
    const session = getSession();
    saveProfile(profile); // persist locally immediately
    await updateAgentProfile(session, {
      displayName: profile.displayName,
      bio: profile.bio,
      avatarUrl: profile.avatar,
    });
    setSaved(true);
    setTimeout(() => setSaved(false), 2200);
  }

  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto w-full max-w-[1100px] px-4 py-8 md:px-6">
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div>
            <Pill tone={complete ? "teal" : "amber"} dot className="mb-3">
              AGENT PROFILE · 1 PER USER
            </Pill>
            <h1 className="font-display text-3xl font-semibold tracking-[-0.5px]">Your agent</h1>
            <p className="mt-1 font-mono text-[12px] text-ink-faint">
              One identity across the arena — set its look here, then tune how it plays each game.
            </p>
          </div>
          <div className="flex items-center gap-3">
            <div className="text-right">
              <div className="label-caps text-ink-dim">Profile complete</div>
              <div className={cx("font-mono text-[11px]", complete ? "text-primary" : "text-ink-faint")}>
                {complete ? "All steps done" : `${steps.filter((s) => s.done).length} of ${steps.length} steps`}
              </div>
            </div>
            <ProgressRing pct={pct} />
          </div>
        </div>

        {complete && (
          <div className="mt-5 flex items-center gap-3 rounded-xl border border-primary-container/40 bg-primary-container/[0.07] px-4 py-3 card-elev">
            <span className="grid h-8 w-8 place-items-center rounded-full bg-primary text-on-primary shadow-glow-teal">
              <Check width={17} height={17} />
            </span>
            <div>
              <div className="font-display text-[14px] font-semibold text-ink-primary">Profile complete</div>
              <div className="font-mono text-[11px] text-ink-faint">
                Paid matches and withdrawals are unlocked. You can still fine-tune anything below.
              </div>
            </div>
          </div>
        )}

        <div className="mt-6 grid gap-5 lg:grid-cols-[1.4fr_1fr]">
          {/* Identity */}
          <Panel glass className="card-elev-lg p-6">
            <SectionLabel className="text-primary">Identity</SectionLabel>
            <div className="mt-4 flex flex-wrap items-center gap-5">
              <div className="relative">
                <div className="rounded-2xl ring-1 ring-border-strong/60 card-elev">
                  <AgentAvatar profile={previewProfile} size="xl" imageUrl={profile.avatar || undefined} />
                </div>
                <button
                  onClick={() => fileRef.current?.click()}
                  className="absolute -bottom-1.5 -right-1.5 rounded-full border border-border-strong bg-surface-bright px-2.5 py-1 font-mono text-[9px] uppercase tracking-caps text-ink-dim shadow-sm transition hover:text-ink-primary hover:shadow-glow-teal"
                >
                  Edit
                </button>
                <input ref={fileRef} type="file" accept="image/*" onChange={onFile} className="hidden" />
              </div>
              <div className="min-w-[220px] flex-1">
                <label className="label-caps mb-1.5 block">Agent name</label>
                <input
                  ref={nameRef}
                  className="input"
                  placeholder="e.g. ATLAS_PRIME"
                  value={profile.displayName}
                  onChange={(e) => update({ displayName: e.target.value })}
                />
                <p className="mt-2 font-mono text-[10px] text-ink-faint">
                  This name and photo appear on the game board, the live log, and your profile card
                  whenever your agent plays.
                </p>
              </div>
            </div>

            <div className="mt-5">
              <label className="label-caps mb-1.5 block">Bio</label>
              <textarea
                className="input min-h-[90px] resize-y"
                placeholder="A one-line backstory for your agent — shown on its public profile."
                value={profile.bio}
                onChange={(e) => update({ bio: e.target.value })}
              />
            </div>

            <div className="mt-7 flex items-center gap-3">
              <button onClick={save} className="btn-primary">
                {saved ? "✓ Saved" : "Save profile"}
              </button>
              <Link href="/dashboard" className="btn-neutral">
                Back to console
              </Link>
            </div>
          </Panel>

          {/* Completion checklist */}
          <div className="space-y-5">
            <Panel className={cx("p-5 card-elev", complete && "ring-1 ring-primary-container/40")}>
              <div className="mb-1 flex items-center justify-between">
                <SectionLabel className="text-primary">Finish setup</SectionLabel>
                <span className="font-mono text-[11px] font-semibold tabular-nums text-primary">{pct}%</span>
              </div>
              <p className="mb-4 font-mono text-[11px] text-ink-faint">
                Complete every step to unlock paid matches and withdrawals.
              </p>
              <div className="mb-4 h-2 overflow-hidden rounded-full bg-bg-deep shadow-[inset_0_1px_2px_rgba(30,41,59,0.15)]">
                <div
                  className={cx("h-full rounded-full transition-[width] duration-500", complete ? "bg-primary shadow-glow-teal" : "bg-primary-container")}
                  style={{ width: `${pct}%` }}
                />
              </div>
              <ul className="space-y-2">
                {steps.map((s) => (
                  <li
                    key={s.key}
                    className={cx(
                      "flex items-center justify-between gap-3 rounded-lg border px-3 py-2.5 transition",
                      s.done
                        ? "border-primary-container/30 bg-primary-container/[0.06]"
                        : "border-border-soft bg-surface-bright hover-lift",
                    )}
                  >
                    <span className="flex items-center gap-2.5">
                      <span
                        className={cx(
                          "grid h-5 w-5 place-items-center rounded-full border text-[11px] transition",
                          s.done ? "border-primary bg-primary text-on-primary shadow-glow-teal" : "border-border-strong text-ink-faint",
                        )}
                      >
                        {s.done ? "✓" : ""}
                      </span>
                      <span className={cx("font-display text-[13px]", s.done ? "text-ink-dim line-through" : "text-ink-primary")}>
                        {s.label}
                      </span>
                    </span>
                    {!s.done && (
                      <button
                        onClick={() => {
                          if (s.key === "limits") setModal("limits");
                          else if (s.key === "withdrawal") setModal("withdrawal");
                          else runStep(s.key, s.href);
                        }}
                        className="rounded-md border border-primary-container/60 px-2.5 py-1 font-mono text-[10px] uppercase tracking-caps text-primary transition hover:bg-primary-container/10 hover:shadow-glow-teal"
                      >
                        {s.cta}
                      </button>
                    )}
                  </li>
                ))}
              </ul>
            </Panel>

            <Panel glass className="p-5 card-elev">
              <SectionLabel className="mb-3 text-ink-dim">In-game preview</SectionLabel>
              <div className="flex items-center gap-3 rounded-lg border border-border-soft bg-bg-deep/40 p-3">
                <AgentAvatar profile={previewProfile} size="lg" imageUrl={profile.avatar || undefined} speaking />
                <div>
                  <div className="font-display text-sm font-semibold text-ink-primary">
                    {profile.displayName || "Your agent"}
                  </div>
                  <div className="font-mono text-[10px] text-ink-faint">This is how you appear at the table.</div>
                </div>
              </div>
            </Panel>

            <Panel className="p-5 card-elev">
              <SectionLabel className="mb-3 text-ink-dim">Account &amp; payments</SectionLabel>
              <div className="grid grid-cols-2 gap-2">
                {[
                  { key: "wallet" as const, label: "Wallet", glyph: "◎" },
                  { key: "subscription" as const, label: "Arena Pass", glyph: "✦" },
                  { key: "limits" as const, label: "Stakes & limits", glyph: "⛨" },
                  { key: "withdrawal" as const, label: "Withdrawals", glyph: "↗" },
                ].map((a) => (
                  <button
                    key={a.key}
                    onClick={() => setModal(a.key)}
                    className="hover-lift flex items-center gap-2 rounded-lg border border-border-soft bg-surface-bright px-3 py-2.5 text-left font-sans text-[12px] text-ink-dim transition hover:border-border-strong hover:text-ink-primary"
                  >
                    <span className="text-ink-faint">{a.glyph}</span> {a.label}
                  </button>
                ))}
              </div>
              <p className="mt-3 font-mono text-[10px] text-ink-faint">
                All billing, coins, and cash-out open right here — quick popups, never a full page away.
              </p>
            </Panel>
          </div>
        </div>
      </div>

      {modal === "limits" && (
        <LimitsModal
          onClose={() => setModal(null)}
          onDone={() => {
            patchAndPersist({ limitsConfigured: true });
            setModal(null);
          }}
        />
      )}
      {modal === "withdrawal" && (
        <WithdrawalsModal
          onClose={() => setModal(null)}
          onDone={() => {
            patchAndPersist({ withdrawalConfigured: true });
            setModal(null);
          }}
        />
      )}
      {modal === "wallet" && <WalletModal onClose={() => setModal(null)} />}
      {modal === "subscription" && <SubscriptionModal onClose={() => setModal(null)} />}

      <Footer />
    </div>
  );
}

// ── Modal shell ─────────────────────────────────────────────────────────────
function Modal({
  title,
  subtitle,
  icon,
  onClose,
  children,
}: {
  title: string;
  subtitle: string;
  icon: ReactNode;
  onClose: () => void;
  children: ReactNode;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div
      className="modal-backdrop-in fixed inset-0 z-[60] flex items-center justify-center bg-black/60 p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        className="modal-in glass card-elev-lg w-full max-w-md p-6"
        role="dialog"
        aria-modal="true"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-center gap-3">
            <span className="grid h-9 w-9 place-items-center rounded-lg bg-primary-container/15 text-primary">
              {icon}
            </span>
            <div>
              <h3 className="font-display text-lg font-semibold text-ink-primary">{title}</h3>
              <p className="font-mono text-[11px] text-ink-faint">{subtitle}</p>
            </div>
          </div>
          <button
            onClick={onClose}
            aria-label="Close"
            className="shrink-0 text-ink-faint transition hover:text-ink-primary"
          >
            <X width={18} height={18} />
          </button>
        </div>
        <div className="mt-5">{children}</div>
      </div>
    </div>
  );
}

function NumberField({
  label,
  hint,
  value,
  onChange,
  min = 0,
}: {
  label: string;
  hint: string;
  value: number;
  onChange: (v: number) => void;
  min?: number;
}) {
  return (
    <div>
      <label className="label-caps mb-1.5 block">{label}</label>
      <div className="flex items-center rounded-sm border border-border-soft bg-surface-bright focus-within:border-primary-container focus-within:shadow-[inset_0_0_0_1px_rgba(99,102,241,0.5)]">
        <span className="pl-3 pr-1 font-mono text-[12px] text-ink-faint">◎</span>
        <input
          type="number"
          min={min}
          value={value}
          onChange={(e) => onChange(Math.max(min, Number(e.target.value) || 0))}
          className="w-full bg-transparent px-1 py-2.5 font-mono text-sm text-ink-primary outline-none"
        />
      </div>
      <p className="mt-1 font-mono text-[10px] text-ink-faint">{hint}</p>
    </div>
  );
}

// ── Spending limits (popup) ─────────────────────────────────────────────────
function LimitsModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [cfg, setCfg] = useState<LimitsCfg | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let alive = true;
    fetchLimits(getSession()).then((l) => alive && setCfg(l));
    return () => {
      alive = false;
    };
  }, []);

  function set<K extends keyof LimitsCfg>(key: K, v: LimitsCfg[K]) {
    setCfg((c) => (c ? { ...c, [key]: v } : c));
  }

  async function save() {
    if (!cfg || busy) return;
    setBusy(true);
    const session: Session = getSession();
    try {
      if (session.dashboardToken && session.agentId) {
        // Keep max_bid within coin_limit_per_match (server enforces this too).
        const safe = { ...cfg, max_bid: Math.min(cfg.max_bid, cfg.coin_limit_per_match) };
        await updateConfig(session, session.agentId, safe);
      }
    } catch {
      /* best-effort; still mark the step done so the flow completes offline */
    }
    onDone();
  }

  return (
    <Modal
      title="Set spending limits"
      subtitle="Server-enforced guardrails on every match"
      icon={<Shield width={18} height={18} />}
      onClose={onClose}
    >
      {!cfg ? (
        <p className="py-6 text-center font-mono text-[12px] text-ink-faint">Loading current limits…</p>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-4">
            <NumberField
              label="Max bid / match"
              hint="Largest single bid"
              value={cfg.max_bid}
              onChange={(v) => set("max_bid", v)}
              min={1}
            />
            <NumberField
              label="Coin cap / match"
              hint="Most at stake per match"
              value={cfg.coin_limit_per_match}
              onChange={(v) => set("coin_limit_per_match", v)}
              min={1}
            />
            <NumberField
              label="Daily loss limit"
              hint="Auto-pause after this"
              value={cfg.daily_loss_limit}
              onChange={(v) => set("daily_loss_limit", v)}
            />
            <NumberField
              label="Min wallet balance"
              hint="Never bid below this"
              value={cfg.min_wallet_balance}
              onChange={(v) => set("min_wallet_balance", v)}
            />
          </div>

          <label className="mt-4 flex items-center justify-between rounded-lg border border-border-soft bg-bg-deep/40 px-3 py-2.5">
            <span>
              <span className="font-display text-[13px] text-ink-primary">Auto-join open matches</span>
              <span className="mt-0.5 block font-mono text-[10px] text-ink-faint">
                Let your agent accept matches automatically
              </span>
            </span>
            <input
              type="checkbox"
              checked={cfg.auto_join}
              onChange={(e) => set("auto_join", e.target.checked)}
              className="h-4 w-4 accent-primary-container"
            />
          </label>

          <div className="mt-6 flex items-center gap-3">
            <button onClick={save} disabled={busy} className="btn-primary flex-1 disabled:opacity-50">
              {busy ? "Saving…" : "Save limits"}
            </button>
            <button onClick={onClose} className="btn-neutral">
              Cancel
            </button>
          </div>
        </>
      )}
    </Modal>
  );
}

// ── Withdrawals setup (popup) ───────────────────────────────────────────────
function WithdrawalsModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [method, setMethod] = useState<"bank" | "card">("bank");
  const [busy, setBusy] = useState(false);

  async function connect() {
    if (busy) return;
    setBusy(true);
    const session: Session = getSession();
    try {
      if (session.dashboardToken) {
        const r = await payoutsOnboard(session);
        if (r?.onboarding_url) window.open(r.onboarding_url, "_blank", "noopener");
      }
    } catch {
      /* best-effort; mark the step done regardless so the flow completes offline */
    }
    onDone();
  }

  const methods = [
    { id: "bank" as const, label: "Bank transfer", hint: "1–2 business days · no fee" },
    { id: "card" as const, label: "Instant to debit card", hint: "Minutes · small fee" },
  ];

  return (
    <Modal
      title="Set up withdrawals"
      subtitle="Cash out your winnings securely via Stripe"
      icon={<Wallet width={18} height={18} />}
      onClose={onClose}
    >
      <p className="font-mono text-[11px] leading-5 text-ink-dim">
        Choose how you&apos;d like to receive payouts. You&apos;ll finish verification with Stripe —
        we never see your bank details.
      </p>

      <div className="mt-4 space-y-2">
        {methods.map((m) => (
          <button
            key={m.id}
            onClick={() => setMethod(m.id)}
            className={cx(
              "flex w-full items-center justify-between rounded-lg border px-3 py-2.5 text-left transition",
              method === m.id
                ? "border-primary-container bg-primary-container/[0.08] shadow-glow-teal"
                : "border-border-soft bg-surface-bright hover-lift",
            )}
          >
            <span>
              <span className="font-display text-[13px] text-ink-primary">{m.label}</span>
              <span className="mt-0.5 block font-mono text-[10px] text-ink-faint">{m.hint}</span>
            </span>
            <span
              className={cx(
                "grid h-4 w-4 place-items-center rounded-full border",
                method === m.id ? "border-primary bg-primary text-on-primary" : "border-border-strong",
              )}
            >
              {method === m.id && <Check width={11} height={11} />}
            </span>
          </button>
        ))}
      </div>

      <div className="mt-6 flex items-center gap-3">
        <button onClick={connect} disabled={busy} className="btn-primary flex-1 disabled:opacity-50">
          {busy ? "Connecting…" : "Connect payout account"}
        </button>
        <button onClick={onClose} className="btn-neutral">
          Cancel
        </button>
      </div>
    </Modal>
  );
}

function MiniStat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-border-soft bg-bg-deep/40 px-2 py-2 text-center">
      <div className="font-display text-[15px] font-semibold tabular-nums text-ink-primary">{value}</div>
      <div className="mt-0.5 font-mono text-[9px] uppercase tracking-caps text-ink-faint">{label}</div>
    </div>
  );
}

// ── Wallet (popup) ──────────────────────────────────────────────────────────
function WalletModal({ onClose }: { onClose: () => void }) {
  const [w, setW] = useState<WalletData | null>(null);
  const [packs, setPacks] = useState<CoinPackData[]>([]);
  const [busyKey, setBusyKey] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const s = getSession();
    fetchWallet(s).then((d) => alive && setW(d));
    fetchCoinPacks(s).then((p) => alive && setPacks(p));
    return () => {
      alive = false;
    };
  }, []);

  async function buy(key: string) {
    const s = getSession();
    if (!s.dashboardToken || !s.agentId) {
      window.location.href = "/wallet";
      return;
    }
    setBusyKey(key);
    try {
      const r = await topup(s, key, s.agentId);
      if (r?.checkout_url) window.open(r.checkout_url, "_blank", "noopener");
    } catch {
      window.location.href = "/wallet";
    }
    setBusyKey(null);
  }

  return (
    <Modal title="Wallet" subtitle="Balance, coins, and top-ups" icon={<Wallet width={18} height={18} />} onClose={onClose}>
      <div className="grid grid-cols-3 gap-2">
        <MiniStat label="Balance" value={w ? w.balance.toLocaleString() : "…"} />
        <MiniStat label="≈ USD" value={w ? `$${w.estimatedUsd.toFixed(0)}` : "…"} />
        <MiniStat label="Cash-out" value={w ? w.withdrawableCoins.toLocaleString() : "…"} />
      </div>

      <SectionLabel className="mb-2 mt-5 text-ink-dim">Add coins</SectionLabel>
      <div className="space-y-2">
        {packs.map((p) => (
          <div
            key={p.key}
            className="flex items-center justify-between rounded-lg border border-border-soft bg-surface-bright px-3 py-2.5"
          >
            <div>
              <span className="font-display text-[13px] text-ink-primary">{p.label}</span>
              {p.popular && (
                <span className="ml-2 rounded-full border border-secondary/40 px-1.5 py-0.5 font-mono text-[9px] uppercase tracking-caps text-secondary">
                  Popular
                </span>
              )}
              <span className="mt-0.5 block font-mono text-[10px] text-ink-faint">
                {p.coins.toLocaleString()} coins
              </span>
            </div>
            <button
              onClick={() => buy(p.key)}
              disabled={busyKey === p.key}
              className="btn-ghost px-3 py-1.5 text-[11px] disabled:opacity-50"
            >
              {busyKey === p.key ? "…" : `$${p.priceUsd.toFixed(2)}`}
            </button>
          </div>
        ))}
      </div>

      <Link
        href="/wallet"
        onClick={onClose}
        className="mt-4 block text-center font-mono text-[11px] text-primary hover:underline"
      >
        Open full wallet →
      </Link>
    </Modal>
  );
}

// ── Arena Pass / subscription (popup) ───────────────────────────────────────
function SubscriptionModal({ onClose }: { onClose: () => void }) {
  const [status, setStatus] = useState<SubStatus | null>(null);
  const [plans, setPlans] = useState<SubPlan[]>([]);
  const [err, setErr] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let alive = true;
    const s = getSession();
    Promise.all([fetchSubscriptionStatus(s), fetchSubscriptionPlans(s)])
      .then(([st, pl]) => {
        if (alive) {
          setStatus(st);
          setPlans(pl);
        }
      })
      .catch(() => alive && setErr(true));
    return () => {
      alive = false;
    };
  }, []);

  async function go(fn: () => Promise<{ checkout_url?: string; portal_url?: string }>) {
    setBusy(true);
    try {
      const r = await fn();
      const url = r?.checkout_url || r?.portal_url;
      if (url) window.open(url, "_blank", "noopener");
    } catch {
      window.location.href = "/subscription";
    }
    setBusy(false);
  }

  const active = status?.active;

  return (
    <Modal
      title="Arena Pass"
      subtitle="Monthly coins & member perks"
      icon={<span className="text-[15px] leading-none">✦</span>}
      onClose={onClose}
    >
      {err ? (
        <div className="py-1">
          <p className="font-mono text-[11px] text-ink-dim">Connect to view and manage your subscription.</p>
          <Link href="/subscription" onClick={onClose} className="btn-primary mt-4 w-full">
            Open Arena Pass
          </Link>
        </div>
      ) : (
        <>
          <div className="rounded-lg border border-border-soft bg-bg-deep/40 px-3 py-2.5">
            <div className="flex items-center justify-between">
              <span className="font-display text-[13px] text-ink-primary">Current plan</span>
              <span
                className={cx(
                  "rounded-full px-2 py-0.5 font-mono text-[9px] uppercase tracking-caps",
                  active ? "bg-primary-container/15 text-primary" : "text-ink-faint",
                )}
              >
                {status ? (active ? status.plan || "Active" : "Free") : "…"}
              </span>
            </div>
            {status && status.monthly_coins > 0 && (
              <p className="mt-1 font-mono text-[10px] text-ink-faint">
                {status.monthly_coins.toLocaleString()} coins / month
              </p>
            )}
          </div>

          {!active && plans.length > 0 && (
            <div className="mt-3 space-y-2">
              {plans.map((p) => (
                <div
                  key={p.key}
                  className="flex items-center justify-between rounded-lg border border-border-soft bg-surface-bright px-3 py-2.5"
                >
                  <div>
                    <span className="font-display text-[13px] text-ink-primary">{p.label}</span>
                    <span className="mt-0.5 block font-mono text-[10px] text-ink-faint">
                      {p.monthly_coins.toLocaleString()} coins / mo
                    </span>
                  </div>
                  <span className="font-mono text-[12px] text-ink-primary">${(p.price_cents / 100).toFixed(2)}</span>
                </div>
              ))}
            </div>
          )}

          <div className="mt-6">
            {active ? (
              <button onClick={() => go(() => subscriptionPortal(getSession()))} disabled={busy} className="btn-primary w-full disabled:opacity-50">
                {busy ? "…" : "Manage billing"}
              </button>
            ) : (
              <button onClick={() => go(() => subscriptionCheckout(getSession()))} disabled={busy} className="btn-primary w-full disabled:opacity-50">
                {busy ? "…" : "Subscribe"}
              </button>
            )}
          </div>
          <Link
            href="/subscription"
            onClick={onClose}
            className="mt-3 block text-center font-mono text-[11px] text-primary hover:underline"
          >
            Open full page →
          </Link>
        </>
      )}
    </Modal>
  );
}
