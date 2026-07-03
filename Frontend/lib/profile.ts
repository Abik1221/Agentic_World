// ---------------------------------------------------------------------------
// Client-side agent profile store.
//
// One user → one agent. The agent's display identity (name, bio, avatar) and
// its per-game *behaviour* live here, persisted in localStorage so the whole
// onboarding/profile flow works end-to-end in the browser even before the Go
// endpoints are wired. lib/api.ts calls the backend first and falls back to
// this store, then mirrors the server response back into it.
//
// Avatar is stored as a data-URL (or http URL) so it can render directly in the
// game UI via <AgentAvatar imageUrl=… />.
// ---------------------------------------------------------------------------

export interface GameBehavior {
  aggression: number; // 0–100
  risk: number; // 0–100 risk tolerance
  bluff: number; // 0–100 deception / bluffing
  chattiness: number; // 0–100 how much it talks / argues
}

export type GameType = "mafia" | "goofspiel";

export interface AgentProfileData {
  displayName: string;
  bio: string;
  avatar: string; // data-URL or http(s) URL; "" when unset
  behaviors: Record<GameType, GameBehavior>;
  behaviorConfigured: boolean; // user has tuned per-game behaviour at least once
  limitsConfigured: boolean; // user has reviewed spending limits
  withdrawalConfigured: boolean; // user has started Stripe payout setup
  updatedAt?: string;
}

const KEY = "aa_profile_v1";
const EVENT = "aa:profilechange";

const DEFAULT_BEHAVIOR: GameBehavior = { aggression: 50, risk: 50, bluff: 50, chattiness: 50 };

export function emptyProfile(): AgentProfileData {
  return {
    displayName: "",
    bio: "",
    avatar: "",
    behaviors: { mafia: { ...DEFAULT_BEHAVIOR }, goofspiel: { ...DEFAULT_BEHAVIOR } },
    behaviorConfigured: false,
    limitsConfigured: false,
    withdrawalConfigured: false,
  };
}

export function getProfile(): AgentProfileData {
  if (typeof window === "undefined") return emptyProfile();
  try {
    const raw = window.localStorage.getItem(KEY);
    if (!raw) return emptyProfile();
    const parsed = JSON.parse(raw) as Partial<AgentProfileData>;
    const base = emptyProfile();
    return {
      ...base,
      ...parsed,
      behaviors: {
        mafia: { ...base.behaviors.mafia, ...parsed.behaviors?.mafia },
        goofspiel: { ...base.behaviors.goofspiel, ...parsed.behaviors?.goofspiel },
      },
    };
  } catch {
    return emptyProfile();
  }
}

export function saveProfile(patch: Partial<AgentProfileData>): AgentProfileData {
  const next: AgentProfileData = { ...getProfile(), ...patch, updatedAt: new Date().toISOString() };
  if (typeof window !== "undefined") {
    window.localStorage.setItem(KEY, JSON.stringify(next));
    window.dispatchEvent(new CustomEvent(EVENT));
  }
  return next;
}

export function setBehavior(game: GameType, behavior: GameBehavior): AgentProfileData {
  const p = getProfile();
  return saveProfile({ behaviors: { ...p.behaviors, [game]: behavior } });
}

/** Subscribe to profile changes (returns an unsubscribe fn). */
export function onProfileChange(fn: () => void): () => void {
  if (typeof window === "undefined") return () => {};
  const handler = () => fn();
  window.addEventListener(EVENT, handler);
  window.addEventListener("storage", handler);
  return () => {
    window.removeEventListener(EVENT, handler);
    window.removeEventListener("storage", handler);
  };
}

export interface CompletionStep {
  key: string;
  label: string;
  done: boolean;
  href: string;
  cta: string;
}

/** Derive the onboarding checklist from an explicit profile + session presence.
 *  Pages that hold live (unsaved) form state pass it here so the checklist and
 *  progress update instantly as the user fills things in. */
export function completionStepsFor(
  p: AgentProfileData,
  opts: { hasSession: boolean },
): CompletionStep[] {
  return [
    { key: "verify", label: "Verify your identity", done: opts.hasSession, href: "/register", cta: "Verify" },
    { key: "name", label: "Name your agent", done: p.displayName.trim().length > 0, href: "/profile", cta: "Add name" },
    { key: "avatar", label: "Upload an agent photo", done: p.avatar.length > 0, href: "/profile", cta: "Upload" },
    { key: "limits", label: "Set spending limits", done: p.limitsConfigured, href: "/guardrails", cta: "Set limits" },
    { key: "withdrawal", label: "Set up withdrawals", done: p.withdrawalConfigured, href: "/withdrawals", cta: "Set up" },
  ];
}

/** Derive the onboarding checklist from the persisted profile + session. */
export function completionSteps(opts: { hasSession: boolean }): CompletionStep[] {
  return completionStepsFor(getProfile(), opts);
}

export function completionPercentFor(p: AgentProfileData, opts: { hasSession: boolean }): number {
  const steps = completionStepsFor(p, opts);
  const done = steps.filter((s) => s.done).length;
  return Math.round((done / steps.length) * 100);
}

export function completionPercent(opts: { hasSession: boolean }): number {
  return completionPercentFor(getProfile(), opts);
}

export function isProfileComplete(opts: { hasSession: boolean }): boolean {
  return completionSteps(opts).every((s) => s.done);
}
