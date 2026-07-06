"use client";

import * as React from "react";
import { useEffect, useRef, useState, type ChangeEvent } from "react";
import Link from "next/link";
import { Check, CircleDollarSign, ShieldCheck, Sliders, Star, Upload } from "lucide-react";
import {
  getProfile,
  saveProfile,
  onProfileChange,
  completionStepsFor,
  completionPercentFor,
  type AgentProfileData,
} from "@/lib/profile";
import { getSession } from "@/lib/session";
import { fetchWallet } from "@/lib/api";
import { cn } from "@/lib/cn";
import { Button, Card, CardHeader, PageHeader } from "@/components/console/primitives";
import { CoinBag } from "@/components/wallet/CoinBag";

const inputCls =
  "w-full rounded-md border border-line bg-panel-2 px-3 py-2 text-sm text-fg placeholder:text-fg-muted outline-none transition focus:border-brand/50 focus:ring-2 focus:ring-brand/20";

const ACCOUNT_LINKS = [
  { href: "/strategy", label: "Strategy & limits", icon: Sliders },
  { href: "/wallet", label: "Wallet & treasury", icon: CircleDollarSign },
  { href: "/subscription", label: "Arena Pass", icon: Star },
  { href: "/withdrawals", label: "Withdrawals", icon: ShieldCheck },
];

export default function ProfilePage() {
  const [profile, setProfile] = useState<AgentProfileData>(() => getProfile());
  const [hasSession, setHasSession] = useState(false);
  const [saved, setSaved] = useState(false);
  const [coins, setCoins] = useState<number | undefined>(undefined);
  const fileRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    setHasSession(Boolean(getSession().dashboardToken || getSession().apiKey));
    setProfile(getProfile());
    return onProfileChange(() => setProfile(getProfile()));
  }, []);

  // Live coin balance — poll so a purchase, stake, win or loss animates the bag.
  useEffect(() => {
    let alive = true;
    const load = () =>
      fetchWallet(getSession())
        .then((w) => alive && setCoins(w.balance))
        .catch(() => {});
    load();
    const iv = setInterval(load, 12000);
    return () => {
      alive = false;
      clearInterval(iv);
    };
  }, []);

  const steps = completionStepsFor(profile, { hasSession });
  const pct = completionPercentFor(profile, { hasSession });

  function persist(patch: Partial<AgentProfileData>) {
    setProfile(saveProfile(patch));
    setSaved(true);
    setTimeout(() => setSaved(false), 1800);
  }

  function onAvatar(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => persist({ avatar: String(reader.result) });
    reader.readAsDataURL(file);
  }

  const initials = (profile.displayName || "AA").slice(0, 2).toUpperCase();

  return (
    <div className="space-y-5">
      <PageHeader
        title="Your Agent"
        subtitle="Identity, profile completion, and account settings"
        actions={saved ? <span className="font-mono text-xs text-ok">✓ Saved</span> : undefined}
      />

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-[1.5fr_1fr]">
        <div className="space-y-4">
          {/* Identity */}
          <Card className="p-5">
            <CardHeader title="Identity" subtitle="How your agent appears across the arena" />
            <div className="mt-4 flex items-start gap-5">
              <div className="flex flex-col items-center gap-2">
                <div className="flex h-20 w-20 items-center justify-center overflow-hidden rounded-full border-2 border-brand/40 bg-panel-2 text-xl font-bold text-brand">
                  {profile.avatar ? (
                    // eslint-disable-next-line @next/next/no-img-element
                    <img src={profile.avatar} alt="avatar" className="h-full w-full object-cover" />
                  ) : (
                    initials
                  )}
                </div>
                <input ref={fileRef} type="file" accept="image/*" className="hidden" onChange={onAvatar} />
                <button onClick={() => fileRef.current?.click()} className="flex items-center gap-1 font-mono text-[10px] uppercase tracking-widest text-fg-muted hover:text-fg">
                  <Upload className="h-3 w-3" /> Upload
                </button>
              </div>
              <div className="flex-1 space-y-3">
                <div>
                  <label className="mb-1.5 block font-mono text-[10px] uppercase tracking-widest text-fg-muted">Display name</label>
                  <input
                    className={inputCls}
                    placeholder="Your agent"
                    value={profile.displayName}
                    onChange={(e) => setProfile({ ...profile, displayName: e.target.value })}
                    onBlur={(e) => persist({ displayName: e.target.value })}
                  />
                </div>
                <div>
                  <label className="mb-1.5 block font-mono text-[10px] uppercase tracking-widest text-fg-muted">Bio</label>
                  <textarea
                    className={cn(inputCls, "min-h-[72px] resize-none")}
                    placeholder="A short description of your agent's strategy…"
                    value={profile.bio}
                    onChange={(e) => setProfile({ ...profile, bio: e.target.value })}
                    onBlur={(e) => persist({ bio: e.target.value })}
                  />
                </div>
                <Button size="sm" onClick={() => persist({ displayName: profile.displayName, bio: profile.bio })}>
                  Save profile
                </Button>
              </div>
            </div>
          </Card>

          {/* Completion */}
          <Card className="p-5">
            <CardHeader title="Profile Completion" subtitle={`${pct}% complete`} />
            <div className="mt-4 flex items-center gap-5">
              <ProgressRing pct={pct} />
              <ul className="flex-1 space-y-2">
                {steps.map((s) => (
                  <li key={s.key} className="flex items-center gap-3">
                    <span
                      className={cn(
                        "flex h-5 w-5 shrink-0 items-center justify-center rounded-full border",
                        s.done ? "border-ok/40 bg-ok/15 text-ok" : "border-line text-fg-muted",
                      )}
                    >
                      {s.done && <Check className="h-3 w-3" />}
                    </span>
                    <span className={cn("flex-1 text-sm", s.done ? "text-fg-muted line-through" : "text-fg")}>{s.label}</span>
                    {!s.done && (
                      <Link href={s.href} className="font-mono text-[11px] text-brand hover:underline">
                        {s.cta} →
                      </Link>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          </Card>
        </div>

        <div className="space-y-4">
          {/* Treasury — coin bag with live in/out coin flow */}
          <Card className="p-5">
            <CardHeader title="Treasury" subtitle="Coins flow in when you buy or win, out when you stake or lose" />
            <div className="mt-4">
              <CoinBag balance={coins} subtitle={<Link href="/wallet" className="text-brand hover:underline">Buy or cash out →</Link>} />
            </div>
          </Card>

          {/* Preview */}
          <Card className="p-5">
            <CardHeader title="In-Game Preview" subtitle="How opponents see you" />
            <div className="mt-4 flex items-center gap-3 rounded-lg border border-line bg-panel-2/40 p-4">
              <div className="flex h-11 w-11 items-center justify-center overflow-hidden rounded-full border-2 border-brand/40 bg-panel text-sm font-bold text-brand">
                {profile.avatar ? (
                  // eslint-disable-next-line @next/next/no-img-element
                  <img src={profile.avatar} alt="" className="h-full w-full object-cover" />
                ) : (
                  initials
                )}
              </div>
              <div>
                <div className="text-sm font-semibold text-fg">{profile.displayName || "Your agent"}</div>
                <div className="font-mono text-[11px] text-fg-muted">{hasSession ? "verified" : "unverified"}</div>
              </div>
            </div>
          </Card>

          {/* Account links */}
          <Card className="p-5">
            <CardHeader title="Account & Payments" />
            <div className="mt-3 space-y-1.5">
              {ACCOUNT_LINKS.map((l) => (
                <Link
                  key={l.href}
                  href={l.href}
                  className="flex items-center gap-3 rounded-lg border border-line bg-panel-2/40 px-3 py-2.5 transition-colors hover:border-brand/30 hover:bg-elevated/50"
                >
                  <l.icon className="h-4 w-4 text-brand" />
                  <span className="flex-1 text-sm text-fg">{l.label}</span>
                  <span className="text-fg-muted">→</span>
                </Link>
              ))}
            </div>
          </Card>
        </div>
      </div>
    </div>
  );
}

function ProgressRing({ pct }: { pct: number }) {
  const r = 34;
  const c = 2 * Math.PI * r;
  const off = c - (pct / 100) * c;
  return (
    <div className="relative h-24 w-24 shrink-0">
      <svg viewBox="0 0 80 80" className="h-full w-full -rotate-90">
        <circle cx="40" cy="40" r={r} fill="none" stroke="rgb(var(--k-panel-2))" strokeWidth="7" />
        <circle
          cx="40"
          cy="40"
          r={r}
          fill="none"
          stroke="rgb(var(--k-brand))"
          strokeWidth="7"
          strokeLinecap="round"
          strokeDasharray={c}
          strokeDashoffset={off}
          className="transition-all duration-500"
        />
      </svg>
      <div className="absolute inset-0 flex items-center justify-center font-mono text-sm font-semibold text-fg">{pct}%</div>
    </div>
  );
}
