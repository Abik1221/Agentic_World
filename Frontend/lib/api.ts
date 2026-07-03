// ---------------------------------------------------------------------------
// API client for the Agent Arena Go backend.
//
// This is the single integration layer between the Next.js UI and the Go API
// (see API_INTEGRATION_GAPS.md). Each exported `fetch*` function calls a real
// endpoint and maps the response into the UI types declared in lib/mock.ts.
//
// Resilience: every function falls back to the mock data if the request fails
// (backend down, unauthenticated, network error) so the UI always renders. Set
// NEXT_PUBLIC_API_STRICT=1 to disable fallback and surface errors instead.
//
// Auth model: the backend issues a dashboard JWT (ScopeUser) and an agent API
// key (ScopeAgent). Pass the appropriate token per call; see lib/session.ts.
// ---------------------------------------------------------------------------
import * as mock from "./mock";
import type {
  Agent,
  CoinPack,
  Engagement,
  LeaderRow,
  LiveMatch,
} from "./mock";
import type { Session } from "./session";

export const API_BASE = (
  process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080"
).replace(/\/$/, "");

const STRICT = process.env.NEXT_PUBLIC_API_STRICT === "1";

export class ApiError extends Error {
  status: number;
  code?: string;
  details?: unknown;
  constructor(status: number, message: string, code?: string, details?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

interface RequestOpts {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  token?: string; // exact bearer token to send (dashboard JWT or agent API key)
  signal?: AbortSignal;
}

export async function apiRequest<T>(path: string, opts: RequestOpts = {}): Promise<T> {
  const { method = "GET", body, token, signal } = opts;
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (token) headers["Authorization"] = `Bearer ${token}`;

  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal,
    cache: "no-store",
  });

  const text = await res.text();
  let data: any = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  if (!res.ok) {
    const code = data?.code ?? data?.error?.code;
    const message = data?.message ?? data?.error?.message ?? res.statusText;
    throw new ApiError(res.status, message, code, data?.details);
  }
  return data as T;
}

/** Wrap a mapper so a failed request degrades to mock data (unless STRICT). */
async function withFallback<T>(label: string, fn: () => Promise<T>, fallback: T): Promise<T> {
  try {
    return await fn();
  } catch (err) {
    if (STRICT) throw err;
    if (typeof console !== "undefined") {
      console.warn(`[api] ${label} failed, using mock fallback:`, (err as Error)?.message ?? err);
    }
    return fallback;
  }
}

function volume(n: number): string {
  if (n >= 1e9) return (n / 1e9).toFixed(1) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "K";
  return String(n);
}

// ---- Backend response shapes (subset of the Go JSON we consume) -------------

interface BeLeaderRow {
  rank: number;
  agent: string;
  slug: string;
  name: string;
  elo: number;
  wins: number;
  losses: number;
  ties: number;
  coins_earned: number;
  current_streak: number;
}
interface BeWallet {
  agent: string;
  balance: number;
  limits: Record<string, number | boolean>;
  usage: {
    loss_today: number;
    loss_session: number;
    active_matches: number;
    daily_headroom: number;
    session_headroom: number;
    concurrent_free: number;
  };
}
interface BeStats {
  agent: string;
  season: number;
  stats: {
    matches: number;
    wins: number;
    losses: number;
    ties: number;
    win_rate: number;
    elo: number;
    coins_earned: number;
    current_streak: number;
    best_streak: number;
  };
  style?: string;
  recent_matches: {
    match_id: string;
    result: "win" | "loss" | "tie";
    your_score: number;
    opp_score: number;
    coins_delta: number;
    opponent: string;
    opponent_elo: number;
    finished_at: string;
  }[];
}
interface BePack {
  key: string;
  label: string;
  price_cents: number;
  coins: number;
}
interface BeLiveMatch {
  match_id: string;
  agents: string[];
  bid: number;
  round: number;
  total_rounds: number;
  scores: [number, number];
}
interface BeLiveStats {
  matches_today: number;
  coins_wagered_today: number;
  biggest_win_today: number;
  active_agents?: number;
}

// ---- Public reads -----------------------------------------------------------

/** GET /v1/leaderboard — season standings (public). */
export function fetchLeaderboard(session?: Session): Promise<LeaderRow[]> {
  return withFallback<LeaderRow[]>(
    "leaderboard",
    async () => {
      const r = await apiRequest<{ entries: BeLeaderRow[] }>("/v1/leaderboard");
      return (r.entries ?? []).map((e) => {
        const games = e.wins + e.losses;
        return {
          rank: e.rank,
          name: e.name,
          owner: "@" + (e.slug || e.agent),
          rating: e.elo,
          rd: 0, // not exposed on the leaderboard payload
          wins: e.wins,
          losses: e.losses,
          winrate: games > 0 ? Math.round((e.wins / games) * 1000) / 10 : 0,
          earned: e.coins_earned,
          trend: 0, // delta not exposed; render flat
          you: session?.agentId ? e.agent === session.agentId : undefined,
        } satisfies LeaderRow;
      });
    },
    mock.leaderboard,
  );
}

/** GET /v1/matches/live — live arena feed (public). */
export function fetchLiveMatches(): Promise<LiveMatch[]> {
  return withFallback(
    "matches/live",
    async () => {
      const r = await apiRequest<{ matches: BeLiveMatch[] }>("/v1/matches/live");
      return (r.matches ?? []).map((m) => ({
        id: m.match_id,
        a: m.agents?.[0] ?? "AGENT_A",
        b: m.agents?.[1] ?? "AGENT_B",
        stake: m.bid,
        pot: m.bid * 2,
        block: "#" + (m.match_id || "").slice(-7),
        meta: `ROUND ${m.round}/${m.total_rounds}`,
        metaTone: "blue" as const,
      }));
    },
    mock.liveMatches,
  );
}

export interface RawLiveMatch {
  matchId: string;
  agents: string[];
  round: number;
  totalRounds: number;
}

/** GET /v1/matches/live with NO mock fallback — used to discover a real match to
 *  stream. Returns [] when the backend is offline so callers can fall back. */
export async function fetchLiveMatchesRaw(signal?: AbortSignal): Promise<RawLiveMatch[]> {
  try {
    const r = await apiRequest<{ matches: BeLiveMatch[] }>("/v1/matches/live", { signal });
    return (r.matches ?? []).map((m) => ({
      matchId: m.match_id,
      agents: m.agents ?? [],
      round: m.round,
      totalRounds: m.total_rounds,
    }));
  } catch {
    return [];
  }
}

/** GET /v1/stats/live (+ matches/live count) — landing/lobby ticker (public). */
export function fetchArenaStats(): Promise<typeof mock.arenaStats> {
  return withFallback(
    "stats/live",
    async () => {
      const [s, live] = await Promise.all([
        apiRequest<BeLiveStats>("/v1/stats/live"),
        apiRequest<{ matches: BeLiveMatch[] }>("/v1/matches/live").catch(() => ({ matches: [] })),
      ]);
      return {
        matchesToday: s.matches_today,
        biggestWin: s.biggest_win_today,
        activeAgents: s.active_agents ?? mock.arenaStats.activeAgents,
        totalVolume: volume(s.coins_wagered_today),
        liveMatches: live.matches?.length ?? mock.arenaStats.liveMatches,
      };
    },
    mock.arenaStats,
  );
}

/** Spectator header view, sourced from the live feed. The per-round bid history
 *  is now driven live from the SSE endpoint GET /v1/match/{id}/watch inside
 *  SpectateLive (see app/spectate/SpectateLive.tsx); the `recentBids` returned
 *  here is only an initial illustrative seed shown before the stream connects. */
export function fetchSpectate(): Promise<{ liveMatch: typeof mock.liveMatch; recentBids: typeof mock.recentBids }> {
  return withFallback(
    "spectate",
    async () => {
      const r = await apiRequest<{ matches: BeLiveMatch[] }>("/v1/matches/live");
      const lm = r.matches?.[0];
      if (!lm) return { liveMatch: mock.liveMatch, recentBids: mock.recentBids };
      return {
        liveMatch: {
          ...mock.liveMatch,
          id: lm.match_id,
          round: lm.round,
          totalRounds: lm.total_rounds,
          pot: lm.bid * 2,
          agentA: { ...mock.liveMatch.agentA, name: lm.agents?.[0] ?? "AGENT_A", score: lm.scores?.[0] ?? 0 },
          agentB: { ...mock.liveMatch.agentB, name: lm.agents?.[1] ?? "AGENT_B", score: lm.scores?.[1] ?? 0 },
        },
        recentBids: mock.recentBids,
      };
    },
    { liveMatch: mock.liveMatch, recentBids: mock.recentBids },
  );
}

// ---- User-scoped reads (dashboard JWT) --------------------------------------

function mapLimits(raw: Record<string, number | boolean> | undefined): typeof mock.limits {
  const L = raw ?? {};
  const num = (snake: string, pascal: string, fb: number) =>
    (L[snake] as number) ?? (L[pascal] as number) ?? fb;
  return {
    coin_limit_per_match: num("coin_limit_per_match", "CoinLimitPerMatch", mock.limits.coin_limit_per_match),
    max_bid: num("max_bid", "MaxBid", mock.limits.max_bid),
    min_wallet_balance: num("min_wallet_balance", "MinWalletBalance", mock.limits.min_wallet_balance),
    daily_loss_limit: num("daily_loss_limit", "DailyLossLimit", mock.limits.daily_loss_limit),
    session_loss_limit: num("session_loss_limit", "SessionLossLimit", mock.limits.session_loss_limit),
    max_concurrent_matches: num("max_concurrent_matches", "MaxConcurrentMatches", mock.limits.max_concurrent_matches),
    cooldown_losses: num("cooldown_losses", "CooldownLosses", mock.limits.cooldown_losses),
    cooldown_seconds: num("cooldown_seconds", "CooldownSeconds", mock.limits.cooldown_seconds),
    // AutoJoin is owner config, not part of the wallet limits payload.
    auto_join: (L["auto_join"] as boolean) ?? (L["AutoJoin"] as boolean) ?? mock.limits.auto_join,
  };
}

/** GET /v1/wallet (+ /v1/wallet/withdrawable) — balance & live usage (user). */
export function fetchWallet(session?: Session): Promise<typeof mock.wallet> {
  return withFallback(
    "wallet",
    async () => {
      const token = session?.dashboardToken;
      const w = await apiRequest<BeWallet>("/v1/wallet", { token });
      let withdrawable = mock.wallet.withdrawableCoins;
      try {
        const wd = await apiRequest<{ withdrawable_coins: number }>("/v1/wallet/withdrawable", { token });
        withdrawable = wd.withdrawable_coins;
      } catch {
        /* withdrawable is best-effort */
      }
      return {
        balance: w.balance,
        currency: "CRD",
        estimatedUsd: w.balance / 10,
        withdrawableCoins: withdrawable,
        usage: {
          lossToday: w.usage?.loss_today ?? 0,
          lossSession: w.usage?.loss_session ?? 0,
          activeMatches: w.usage?.active_matches ?? 0,
          headroom: w.usage?.daily_headroom ?? 0,
        },
      };
    },
    mock.wallet,
  );
}

/** Current agent limits, read from the wallet payload (user scope). */
export function fetchLimits(session?: Session): Promise<typeof mock.limits> {
  return withFallback(
    "limits",
    async () => {
      const w = await apiRequest<BeWallet>("/v1/wallet", { token: session?.dashboardToken });
      return mapLimits(w.limits);
    },
    mock.limits,
  );
}

/** GET /v1/wallet/packs — coin packs for sale (user). */
export function fetchCoinPacks(session?: Session): Promise<CoinPack[]> {
  return withFallback(
    "wallet/packs",
    async () => {
      const r = await apiRequest<{ packs: BePack[] }>("/v1/wallet/packs", {
        token: session?.dashboardToken,
      });
      return (r.packs ?? []).map((p) => ({
        key: p.key,
        label: p.label,
        coins: p.coins,
        priceUsd: p.price_cents / 100,
        popular: p.key.toLowerCase() === "gold",
      }));
    },
    mock.coinPacks,
  );
}

// ---- Agent-scoped reads (agent API key) -------------------------------------

export interface DashboardData {
  userAgent: Agent;
  recentEngagements: Engagement[];
  performanceBars: number[];
}

/** GET /v1/agent/stats — the owner's agent stats + recent matches (agent key). */
export function fetchDashboard(session?: Session): Promise<DashboardData> {
  const fallback: DashboardData = {
    userAgent: mock.userAgent,
    recentEngagements: mock.recentEngagements,
    performanceBars: mock.performanceBars,
  };
  return withFallback(
    "agent/stats",
    async () => {
      const s = await apiRequest<BeStats>("/v1/agent/stats", { token: session?.apiKey });
      const st = s.stats;
      const userAgent: Agent = {
        id: s.agent,
        name: session?.agentName ?? s.agent,
        version: mock.userAgent.version, // not exposed by stats
        rating: st.elo,
        rd: mock.userAgent.rd, // Glicko RD not in this payload
        wins: st.wins,
        losses: st.losses,
        draws: st.ties,
        streak: st.current_streak,
        status: "online",
        aggression: mock.userAgent.aggression, // style metrics not exposed numerically
        efficiency: mock.userAgent.efficiency,
      };
      const recent = s.recent_matches ?? [];
      const recentEngagements: Engagement[] = recent.map((m) => ({
        id: m.match_id,
        opponent: m.opponent,
        result: m.result === "tie" ? "draw" : m.result,
        reward: m.coins_delta,
        elo: 0, // per-match elo delta not exposed
      }));
      const performanceBars =
        recent.length > 0
          ? recent
              .slice(0, 13)
              .reverse()
              .map((m) => (m.result === "win" ? 8 : m.result === "loss" ? 3 : 5))
          : mock.performanceBars;
      return { userAgent, recentEngagements, performanceBars };
    },
    fallback,
  );
}

// ---- Writes / actions -------------------------------------------------------

export interface RegisterResult {
  claim_token: string;
  expires_at: string;
  instructions?: string;
}

/** POST /v1/register — create owner + agent, returns a claim token (public). */
export function register(agentName: string, description: string): Promise<RegisterResult> {
  return apiRequest<RegisterResult>("/v1/register", {
    method: "POST",
    body: { agent_name: agentName, description },
  });
}

// ---- Email + password auth (public) -----------------------------------------
// When the Go API is reachable these hit the real /v1/auth endpoints, so real
// errors (wrong password, email already taken) surface to the user. When the API
// is unreachable (backend not running) they fall back to a local DEMO session so
// the flow is still usable in a frontend-only dev setup — mirroring the mock
// fallback every other call in this file uses.
//
// The distinction is precise: apiRequest throws an ApiError only when the backend
// *responded* with an error status; a genuine network failure throws a plain
// TypeError. We rethrow ApiError (and always rethrow under NEXT_PUBLIC_API_STRICT)
// and only fall back on the network-failure case.

export interface SignupResult {
  dashboard_token: string;
  api_key: string; // shown exactly once
  agent_id: string;
  agent_name: string;
}

function randId(prefix: string, n = 10): string {
  return `${prefix}${Math.random().toString(36).slice(2, 2 + n)}`;
}

/** POST /v1/auth/signup — create an email+password owner + agent (rate-limited). */
export async function signup(input: {
  email: string;
  password: string;
  agentName: string;
  description?: string;
}): Promise<SignupResult> {
  try {
    return await apiRequest<SignupResult>("/v1/auth/signup", {
      method: "POST",
      body: {
        email: input.email,
        password: input.password,
        agent_name: input.agentName,
        description: input.description ?? "",
      },
    });
  } catch (err) {
    if (STRICT || err instanceof ApiError) throw err; // backend spoke — respect it
    // Offline/dev fallback: mint a local demo session so sign-up still works.
    return {
      dashboard_token: randId("dev-dash-"),
      api_key: randId("sk_arena_dev_", 14),
      agent_id: randId("ag_"),
      agent_name: input.agentName,
    };
  }
}

export interface LoginResult {
  dashboard_token: string;
  agent_id: string;
  agent_name: string;
}

/** POST /v1/auth/login — email+password sign-in, returns a dashboard session. */
export async function login(email: string, password: string): Promise<LoginResult> {
  try {
    return await apiRequest<LoginResult>("/v1/auth/login", {
      method: "POST",
      body: { email, password },
    });
  } catch (err) {
    if (STRICT || err instanceof ApiError) throw err; // e.g. 401 wrong password
    // Offline/dev fallback: accept the credentials locally so the console opens.
    return {
      dashboard_token: randId("dev-dash-"),
      agent_id: randId("ag_"),
      agent_name: email.split("@")[0] || "agent",
    };
  }
}

export interface VerifyResult {
  api_key: string;
  agent_id: string;
  dashboard_token: string;
}

/** GET /v1/register/verify — poll claim; 202 while pending, 200 when verified. */
export function verifyClaim(claimToken: string, captcha = "dev"): Promise<VerifyResult> {
  const q = new URLSearchParams({ claim_token: claimToken, captcha });
  return apiRequest<VerifyResult>(`/v1/register/verify?${q.toString()}`);
}

/** POST /v1/agent/config — owner-only spending limits (dashboard JWT). */
export function updateConfig(
  session: Session,
  agentId: string,
  cfg: typeof mock.limits,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>("/v1/agent/config", {
    method: "POST",
    token: session.dashboardToken,
    body: {
      agent_id: agentId,
      coin_limit_per_match: cfg.coin_limit_per_match,
      daily_loss_limit: cfg.daily_loss_limit,
      session_loss_limit: cfg.session_loss_limit,
      min_wallet_balance: cfg.min_wallet_balance,
      max_bid: cfg.max_bid,
      max_concurrent_matches: cfg.max_concurrent_matches,
      cooldown_losses: cfg.cooldown_losses,
      cooldown_seconds: cfg.cooldown_seconds,
      auto_join: cfg.auto_join,
    },
  });
}

/** POST /v1/wallet/topup — start a Stripe checkout for a coin pack (user). */
export function topup(
  session: Session,
  pack: string,
  agentId: string,
  paymentMethod = "card",
): Promise<{ checkout_url: string; session_id: string }> {
  return apiRequest("/v1/wallet/topup", {
    method: "POST",
    token: session.dashboardToken,
    body: { pack, agent: agentId, payment_method: paymentMethod },
  });
}

export interface DepositQuote {
  pack_key: string;
  pack_label: string;
  coins: number;
  pack_cents: number;
  processing_fee_cents: number;
  total_cents: number;
  payment_method: string;
  currency: string;
}

/** GET /v1/wallet/packs/{key}/quote — deposit fee breakdown before checkout. */
export function fetchDepositQuote(
  session: Session,
  packKey: string,
  paymentMethod = "card",
): Promise<DepositQuote> {
  const q = new URLSearchParams({ payment_method: paymentMethod });
  return apiRequest(`/v1/wallet/packs/${encodeURIComponent(packKey)}/quote?${q}`, {
    token: session.dashboardToken,
  });
}

/** POST /v1/wallet/topup/confirm — dev-only: credit coins after offline checkout. */
export function confirmTopup(session: Session, sessionId: string): Promise<{ status: string }> {
  return apiRequest("/v1/wallet/topup/confirm", {
    method: "POST",
    token: session.dashboardToken,
    body: { session_id: sessionId },
  });
}

export interface UserWalletSummary {
  user: string;
  available_balance: number;
  locked_balance: number;
  pending_balance: number;
  lifetime_deposits: number;
  lifetime_withdrawals: number;
  tournament_winnings: number;
  lifetime_earnings: number;
  coin_cents: number;
  agents: {
    agent: string;
    name: string;
    balance: number;
    locked_in_matches: number;
    withdrawable: number;
    active_matches: number;
  }[];
}

export interface WalletTxn {
  txn_id: string;
  kind: string;
  amount: number;
  created_at: string;
}

/** GET /v1/user/wallet — owner treasury + agent balances (user). */
export function fetchUserWallet(session?: Session): Promise<UserWalletSummary> {
  return withFallback(
    "user/wallet",
    () => apiRequest<UserWalletSummary>("/v1/user/wallet", { token: session?.dashboardToken }),
    {
      user: "",
      available_balance: mock.wallet.balance,
      locked_balance: 0,
      pending_balance: 0,
      lifetime_deposits: 0,
      lifetime_withdrawals: 0,
      tournament_winnings: 0,
      lifetime_earnings: 0,
      coin_cents: 1,
      agents: [],
    },
  );
}

/** GET /v1/user/wallet/history — treasury transaction history (user). */
export function fetchUserWalletHistory(session: Session, limit = 50): Promise<WalletTxn[]> {
  return withFallback(
    "user/wallet/history",
    async () => {
      const r = await apiRequest<{ transactions: WalletTxn[] }>(
        `/v1/user/wallet/history?limit=${limit}`,
        { token: session.dashboardToken },
      );
      return r.transactions ?? [];
    },
    [],
  );
}

/** POST /v1/wallet/allocate — move coins from treasury to an agent (user). */
export function allocateToAgent(
  session: Session,
  agentId: string,
  amount: number,
): Promise<{ agent: string; allocated: number }> {
  return apiRequest("/v1/wallet/allocate", {
    method: "POST",
    token: session.dashboardToken,
    body: { agent: agentId, amount },
  });
}

/** GET /v1/withdrawals — list recent withdrawal requests (user). */
export function fetchWithdrawals(session: Session, limit = 20): Promise<Withdrawal[]> {
  return withFallback(
    "withdrawals/list",
    async () => {
      const r = await apiRequest<{ withdrawals: Withdrawal[] }>(`/v1/withdrawals?limit=${limit}`, {
        token: session.dashboardToken,
      });
      return r.withdrawals ?? [];
    },
    [],
  );
}

export interface AdminWithdrawal extends Withdrawal {
  owner: string;
  agent_name?: string;
  can_approve: boolean;
  clearing_wait_ms: number;
}

/** GET /v1/admin/withdrawals — operator approval queue (admin). */
export function fetchAdminWithdrawals(session: Session, status = "requested"): Promise<AdminWithdrawal[]> {
  return apiRequest<{ withdrawals: AdminWithdrawal[] }>(
    `/v1/admin/withdrawals?status=${encodeURIComponent(status)}`,
    { token: session.dashboardToken },
  ).then((r) => r.withdrawals ?? []);
}

export function approveWithdrawal(session: Session, id: string): Promise<{ status: string }> {
  return apiRequest(`/v1/admin/withdrawals/${encodeURIComponent(id)}/approve`, {
    method: "POST",
    token: session.dashboardToken,
  });
}

export function rejectWithdrawal(session: Session, id: string, reason: string): Promise<{ status: string }> {
  return apiRequest(`/v1/admin/withdrawals/${encodeURIComponent(id)}/reject`, {
    method: "POST",
    token: session.dashboardToken,
    body: { reason },
  });
}

// ---- Arena Pass subscription (user) -------------------------------------------

export interface SubscriptionPlan {
  key: string;
  label: string;
  price_cents: number;
  monthly_coins: number;
  currency: string;
}

export interface SubscriptionStatus {
  plan: string;
  status: string;
  active: boolean;
  monthly_coins: number;
  current_period_end?: string;
  manage_billing_available: boolean;
}

export function fetchSubscriptionPlans(session: Session): Promise<SubscriptionPlan[]> {
  return apiRequest<{ plans: SubscriptionPlan[] }>("/v1/subscription/plans", {
    token: session.dashboardToken,
  }).then((r) => r.plans ?? []);
}

export function fetchSubscriptionStatus(session: Session): Promise<SubscriptionStatus> {
  return apiRequest<SubscriptionStatus>("/v1/subscription", { token: session.dashboardToken });
}

export function subscriptionCheckout(session: Session): Promise<{ checkout_url: string; session_id: string }> {
  return apiRequest("/v1/subscription/checkout", { method: "POST", token: session.dashboardToken });
}

export function subscriptionPortal(session: Session): Promise<{ portal_url: string }> {
  return apiRequest("/v1/subscription/portal", { method: "POST", token: session.dashboardToken });
}

export function confirmSubscription(session: Session, sessionId: string): Promise<{ status: string }> {
  return apiRequest("/v1/subscription/confirm", {
    method: "POST",
    token: session.dashboardToken,
    body: { session_id: sessionId },
  });
}

export interface Clip {
  clip_id: string;
  match_id: string;
  trigger: string;
  round_seq: number;
  asset_url: string;
  share_count: number;
  created_at: string;
}

/** GET /v1/clips/trending — auto-generated highlight clips (public). */
export function fetchClips(): Promise<Clip[]> {
  return withFallback(
    "clips/trending",
    async () => {
      const r = await apiRequest<{ clips: Clip[] }>("/v1/clips/trending");
      return r.clips ?? [];
    },
    [],
  );
}

// ---- Profiles (public) ------------------------------------------------------

export interface ProfileStats {
  matches: number;
  wins: number;
  losses: number;
  ties: number;
  win_rate: number;
  elo: number;
  coins_earned: number;
  current_streak: number;
  best_streak: number;
}
export interface ProfileRecentMatch {
  match_id: string;
  result: "win" | "loss" | "tie";
  your_score: number;
  opp_score: number;
  coins_delta: number;
  opponent: string;
  opponent_elo: number;
  finished_at: string;
}
export interface Profile {
  agent: string;
  name?: string;
  slug?: string;
  x_handle?: string;
  status?: string;
  verification_level?: string;
  season: number;
  stats: ProfileStats;
  style?: string;
  recent_matches: ProfileRecentMatch[];
}

/** GET /v1/agent/{id}/profile — public agent profile + recent matches. */
export function fetchProfile(id: string): Promise<Profile | null> {
  return withFallback(
    "agent/profile",
    () => apiRequest<Profile>(`/v1/agent/${encodeURIComponent(id)}/profile`),
    null,
  );
}

// ---- Social (user) ----------------------------------------------------------

/** POST /v1/agent/{id}/follow — follow an agent (user). */
export function followAgent(session: Session, id: string): Promise<{ following: boolean }> {
  return apiRequest(`/v1/agent/${encodeURIComponent(id)}/follow`, {
    method: "POST",
    token: session.dashboardToken,
  });
}
/** DELETE /v1/agent/{id}/follow — unfollow an agent (user). */
export function unfollowAgent(session: Session, id: string): Promise<{ following: boolean }> {
  return apiRequest(`/v1/agent/${encodeURIComponent(id)}/follow`, {
    method: "DELETE",
    token: session.dashboardToken,
  });
}

// ---- Tournaments ------------------------------------------------------------

export interface Tournament {
  tournament_id: string;
  name: string;
  sponsor?: string;
  prize_pool: number;
  status: string;
  winner?: string;
  entries: number;
}

/** GET /v1/tournaments/{id} — tournament detail (public). */
export function fetchTournament(id: string): Promise<Tournament | null> {
  return withFallback(
    "tournaments/get",
    () => apiRequest<Tournament>(`/v1/tournaments/${encodeURIComponent(id)}`),
    null,
  );
}

// ---- Withdrawals / payouts (user) -------------------------------------------

export interface WithdrawQuote {
  coins: number;
  gross_cents: number;
  platform_fee_coins: number;
  stripe_fee_cents: number;
  net_cents: number;
}
export interface Withdrawal {
  withdrawal_id: string;
  agent: string;
  coins: number;
  fee_coins: number;
  gross_cents: number;
  stripe_fee_cents: number;
  net_cents: number;
  status: string;
  transfer_id?: string;
  requested_at: string;
}

/** GET /v1/wallet/withdrawable — withdrawable balance + payout quote (user). */
export function fetchWithdrawable(
  session: Session,
  agentId?: string,
): Promise<{ withdrawable_coins: number; quote: WithdrawQuote | null }> {
  const agent = agentId ?? session.agentId;
  const q = agent ? `?agent=${encodeURIComponent(agent)}` : "";
  return withFallback(
    "wallet/withdrawable",
    () =>
      apiRequest<{ withdrawable_coins: number; quote: WithdrawQuote | null }>(
        `/v1/wallet/withdrawable${q}`,
        { token: session.dashboardToken },
      ),
    { withdrawable_coins: 0, quote: null },
  );
}

/** GET /v1/withdrawals/{id} — withdrawal status (user). */
export function fetchWithdrawal(session: Session, id: string): Promise<Withdrawal | null> {
  return withFallback(
    "withdrawals/get",
    () => apiRequest<Withdrawal>(`/v1/withdrawals/${encodeURIComponent(id)}`, { token: session.dashboardToken }),
    null,
  );
}

/** POST /v1/withdrawals — file a cash-out request (user). */
export function requestWithdrawal(
  session: Session,
  agentId: string,
  coins: number,
): Promise<Withdrawal> {
  return apiRequest<Withdrawal>("/v1/withdrawals", {
    method: "POST",
    token: session.dashboardToken,
    body: { agent: agentId, coins },
  });
}

/** POST /v1/payouts/onboard — start Stripe Connect onboarding (user). */
export function payoutsOnboard(session: Session): Promise<{ onboarding_url: string }> {
  return apiRequest("/v1/payouts/onboard", {
    method: "POST",
    token: session.dashboardToken,
  });
}

// ---- Disputes (user) --------------------------------------------------------

/** POST /v1/disputes — report a match for review (user). */
export function fileDispute(
  session: Session,
  input: { match: string; agent: string; kind: string; detail: string },
): Promise<{ dispute_id: string }> {
  return apiRequest("/v1/disputes", {
    method: "POST",
    token: session.dashboardToken,
    body: input,
  });
}

// ---- API key & signing key management (user) --------------------------------

/** POST /v1/agent/keys — rotate/create the agent API key (user). Returns it once. */
export function createApiKey(session: Session, agentId: string): Promise<{ api_key: string }> {
  return apiRequest("/v1/agent/keys", {
    method: "POST",
    token: session.dashboardToken,
    body: { agent_id: agentId },
  });
}
/** DELETE /v1/agent/keys/{prefix} — revoke a key by prefix (user). */
export function revokeApiKey(session: Session, prefix: string): Promise<void> {
  return apiRequest(`/v1/agent/keys/${encodeURIComponent(prefix)}`, {
    method: "DELETE",
    token: session.dashboardToken,
  });
}
/** POST /v1/agent/signing-key — register an Ed25519 signing pubkey (user). */
export function setSigningKey(
  session: Session,
  agentId: string,
  pubkey: string,
): Promise<{ status: string }> {
  return apiRequest("/v1/agent/signing-key", {
    method: "POST",
    token: session.dashboardToken,
    body: { agent_id: agentId, pubkey },
  });
}

// ---- Admin / dev ------------------------------------------------------------

/** POST /v1/admin/mint — mint test coins (admin; only when ALLOW_MINT=true). */
export function mintCoins(
  session: Session,
  agentId: string,
  amount: number,
): Promise<unknown> {
  return apiRequest("/v1/admin/mint", {
    method: "POST",
    token: session.dashboardToken,
    body: { agent: agentId, amount },
  });
}

// ---- Match play (agent scope) -----------------------------------------------

export interface LobbyItem {
  match_id: string;
  game: string;
  bid: number;
  creator_agent: string;
  created_at: string;
}
export interface MatchSide {
  hand: number[];
  score: number;
  has_acted?: boolean;
}
export interface MatchRound {
  round: number;
  prize: number;
  prize_pool: number;
  your_card: number;
  opp_card: number;
  winner: "you" | "opponent" | "tie";
}
export interface MatchView {
  match_id: string;
  game: string;
  status: string;
  round: number;
  total_rounds: number;
  current_prize: number;
  prize_pool: number;
  your_turn: boolean;
  deadline?: string;
  you: MatchSide;
  opponent: MatchSide;
  legal_actions: { play_card_from: number[] };
  history: MatchRound[];
  stake: { your_coins: number };
  prize_order_commit: string;
  result?: { winner?: string; your_coins?: number } & Record<string, unknown>;
}

/** GET /v1/lobby — open matches at a bid (agent key). */
export function fetchLobby(session: Session, bid?: number): Promise<LobbyItem[]> {
  return withFallback(
    "lobby",
    async () => {
      const q = new URLSearchParams({ game: "goofspiel" });
      if (bid) q.set("bid", String(bid));
      const r = await apiRequest<{ matches: LobbyItem[] }>(`/v1/lobby?${q.toString()}`, {
        token: session.apiKey,
      });
      return r.matches ?? [];
    },
    [],
  );
}
/** POST /v1/lobby/create — post a new open match (agent key). */
export function createMatch(session: Session, bid: number): Promise<{ match_id: string }> {
  return apiRequest("/v1/lobby/create", {
    method: "POST",
    token: session.apiKey,
    body: { bid },
  });
}
/** POST /v1/lobby/join — join an open match (agent key). */
export function joinMatch(session: Session, matchId: string): Promise<MatchView> {
  return apiRequest<MatchView>("/v1/lobby/join", {
    method: "POST",
    token: session.apiKey,
    body: { match_id: matchId },
  });
}
/** GET /v1/match/{id}/state — current state, optional long-poll (agent key). */
export function fetchMatchState(
  session: Session,
  id: string,
  opts: { wait?: boolean; timeout?: number } = {},
): Promise<MatchView> {
  const q = new URLSearchParams();
  if (opts.wait) q.set("wait", "true");
  if (opts.timeout) q.set("timeout", String(opts.timeout));
  const qs = q.toString();
  return apiRequest<MatchView>(`/v1/match/${encodeURIComponent(id)}/state${qs ? `?${qs}` : ""}`, {
    token: session.apiKey,
  });
}
/** POST /v1/match/{id}/action — play a card (agent key, idempotent per round). */
export function playCard(
  session: Session,
  id: string,
  round: number,
  card: number,
): Promise<MatchView> {
  return apiRequest<MatchView>(`/v1/match/${encodeURIComponent(id)}/action`, {
    method: "POST",
    token: session.apiKey,
    body: { round, card },
  });
}

/** SSE endpoint for spectating a match live (public). Use with EventSource. */
export function watchUrl(id: string): string {
  return `${API_BASE}/v1/match/${encodeURIComponent(id)}/watch`;
}

// ── Mafia (social-deduction spectator game) ─────────────────────────────────

/** SSE endpoint for spectating a Mafia match live (public). Use with EventSource. */
export function mafiaWatchUrl(id: string): string {
  return `${API_BASE}/v1/mafia/${encodeURIComponent(id)}/watch`;
}

export interface MafiaLiveMatch {
  matchId: string;
  title: string;
  agents: string[];
  players: number;
  alive: number;
  day: number;
  phase: string;
  winner?: string;
  watchers: number;
}

/** Live Mafia tables. Empty array when the backend is offline (no mock fallback). */
export async function fetchMafiaLive(signal?: AbortSignal): Promise<MafiaLiveMatch[]> {
  try {
    const data = await apiRequest<{ matches: RawMafiaMatch[] }>("/v1/mafia/live", { signal });
    return (data.matches ?? []).map((m) => ({
      matchId: m.match_id,
      title: m.title,
      agents: m.agents ?? [],
      players: m.players,
      alive: m.alive,
      day: m.day,
      phase: m.phase,
      winner: m.winner || undefined,
      watchers: m.watchers,
    }));
  } catch {
    return [];
  }
}

interface RawMafiaMatch {
  match_id: string;
  title: string;
  agents: string[];
  players: number;
  alive: number;
  day: number;
  phase: string;
  winner?: string;
  watchers: number;
}

// ── Mafia economy, replay & agent-scope play ────────────────────────────────
// These mirror the server-authoritative Mafia engine. The economy/replay reads
// are public; lobby/state/action require the agent API key. The agent view is
// redacted server-side: an agent only ever sees its own role, its fellow-Mafia
// allies (Mafia only), the public transcript, and its OWN private night results.

export interface MafiaEconomy {
  agents: number;
  entryFee: number;
  grossPool: number;
  platformFeePct: number;
  platformFee: number;
  rewardPool: number;
}

export interface MafiaReward {
  seat: number;
  agentId?: string;
  team: "town" | "mafia";
  alive: boolean;
  onWinningTeam: boolean;
  eligible: boolean;
  payout: number;
  reason: string;
}

/** One engine event ({seq,type,payload}); night payloads are stripped while live. */
export interface MafiaLogEvent {
  seq: number;
  type: string;
  payload: Record<string, unknown>;
}

/** Redacted per-seat view returned by join/state/action. */
export interface MafiaAgentView {
  matchId: string;
  status: string; // waiting | active | finished | aborted
  day: number;
  phase: string; // night | morning | discussion | voting | result
  yourSeat: number;
  yourRole?: string; // Mafia | Detective | Doctor | Sheriff | Villager
  alive: Record<string, boolean>;
  allies?: number[]; // fellow Mafia seats (Mafia agents only)
  legal?: string[]; // action kinds valid for this seat right now
  transcript?: MafiaLogEvent[]; // shared public events this seat may see
  private?: MafiaLogEvent[]; // this seat's own night results only
  deadline?: string;
  entryFee: number;
  economy: MafiaEconomy;
  result?: { winner: string; rewards: MafiaReward[] };
}

export interface MafiaLobbyItem {
  match_id: string;
  entry_fee: number;
  seats_filled: number;
  seats_total: number;
  creator_agent: string;
  created_at: string;
}

function mapMafiaEconomy(e: any): MafiaEconomy {
  return {
    agents: e?.agents ?? 0,
    entryFee: e?.entry_fee ?? 0,
    grossPool: e?.gross_pool ?? 0,
    platformFeePct: e?.platform_fee_pct ?? 0,
    platformFee: e?.platform_fee ?? 0,
    rewardPool: e?.reward_pool ?? 0,
  };
}

function mapMafiaReward(r: any): MafiaReward {
  return {
    seat: r?.seat ?? 0,
    agentId: r?.agent_id || undefined,
    team: (r?.team as "town" | "mafia") ?? "town",
    alive: Boolean(r?.alive),
    onWinningTeam: Boolean(r?.on_winning_team),
    eligible: Boolean(r?.eligible),
    payout: r?.payout ?? 0,
    reason: String(r?.reason ?? ""),
  };
}

function mapMafiaAgentView(v: any): MafiaAgentView {
  return {
    matchId: v?.match_id ?? "",
    status: v?.status ?? "",
    day: v?.day ?? 0,
    phase: v?.phase ?? "",
    yourSeat: v?.your_seat ?? 0,
    yourRole: v?.your_role || undefined,
    alive: v?.alive ?? {},
    allies: v?.allies ?? undefined,
    legal: v?.legal ?? undefined,
    transcript: v?.public ?? undefined,
    private: v?.private ?? undefined,
    deadline: v?.deadline || undefined,
    entryFee: v?.entry_fee ?? 0,
    economy: mapMafiaEconomy(v?.economy ?? {}),
    result: v?.result
      ? { winner: v.result.winner, rewards: (v.result.rewards ?? []).map(mapMafiaReward) }
      : undefined,
  };
}

/** GET /v1/mafia/{id}/economy — server pool breakdown (+ rewards once finished). */
export async function fetchMafiaEconomy(
  id: string,
  signal?: AbortSignal,
): Promise<{ economy: MafiaEconomy; rewards: MafiaReward[] } | null> {
  try {
    const d = await apiRequest<{ economy: any; rewards: any[] }>(
      `/v1/mafia/${encodeURIComponent(id)}/economy`,
      { signal },
    );
    return { economy: mapMafiaEconomy(d.economy), rewards: (d.rewards ?? []).map(mapMafiaReward) };
  } catch {
    return null;
  }
}

/** GET /v1/mafia/{id}/replay — event log; redacted while live, full once finished. */
export async function fetchMafiaReplay(id: string, signal?: AbortSignal): Promise<MafiaLogEvent[]> {
  try {
    const d = await apiRequest<{ events: MafiaLogEvent[] }>(
      `/v1/mafia/${encodeURIComponent(id)}/replay`,
      { signal },
    );
    return d.events ?? [];
  } catch {
    return [];
  }
}

/** GET /v1/mafia/lobby — open Mafia tables (agent key). */
export function fetchMafiaLobby(session: Session, entryFee?: number): Promise<MafiaLobbyItem[]> {
  return withFallback(
    "mafia-lobby",
    async () => {
      const q = new URLSearchParams();
      if (entryFee) q.set("entry_fee", String(entryFee));
      const qs = q.toString();
      const r = await apiRequest<{ matches: MafiaLobbyItem[] }>(
        `/v1/mafia/lobby${qs ? `?${qs}` : ""}`,
        { token: session.apiKey },
      );
      return r.matches ?? [];
    },
    [],
  );
}

/** POST /v1/mafia/lobby/create — open a new table (agent key). */
export function mafiaCreateTable(session: Session, entryFee: number): Promise<{ match_id: string }> {
  return apiRequest("/v1/mafia/lobby/create", {
    method: "POST",
    token: session.apiKey,
    body: { entry_fee: entryFee },
  });
}

/** POST /v1/mafia/lobby/join — take a seat; returns the redacted view (agent key). */
export async function mafiaJoin(session: Session, matchId: string): Promise<MafiaAgentView> {
  const v = await apiRequest<any>("/v1/mafia/lobby/join", {
    method: "POST",
    token: session.apiKey,
    body: { match_id: matchId },
  });
  return mapMafiaAgentView(v);
}

/** POST /v1/mafia/lobby/cancel — withdraw a table you created while waiting. */
export async function mafiaCancel(session: Session, matchId: string): Promise<void> {
  await apiRequest("/v1/mafia/lobby/cancel", {
    method: "POST",
    token: session.apiKey,
    body: { match_id: matchId },
  });
}

/** GET /v1/mafia/{id}/state — current redacted view for this seat (agent key). */
export async function fetchMafiaAgentState(session: Session, matchId: string): Promise<MafiaAgentView> {
  const v = await apiRequest<any>(`/v1/mafia/${encodeURIComponent(matchId)}/state`, {
    token: session.apiKey,
  });
  return mapMafiaAgentView(v);
}

export interface MafiaActionInput {
  action: string; // night_kill | investigate | protect | profile | message | vote
  target?: number;
  tone?: string;
  text?: string;
}

/** POST /v1/mafia/{id}/action — submit this phase's action (agent key). */
export async function mafiaAct(
  session: Session,
  matchId: string,
  act: MafiaActionInput,
): Promise<MafiaAgentView> {
  const v = await apiRequest<any>(`/v1/mafia/${encodeURIComponent(matchId)}/action`, {
    method: "POST",
    token: session.apiKey,
    body: act,
  });
  return mapMafiaAgentView(v);
}

// ════════════════════════════════════════════════════════════════════════════
// Account & profile flows — magic-link recovery, single-agent profile, per-game
// behaviour, profile completion, notifications.
//
// Each function calls the backend first and falls back to the client profile
// store (lib/profile.ts) so the entire flow is usable in the browser before the
// Go endpoints (see patches/ in the repo root) are deployed.
// ════════════════════════════════════════════════════════════════════════════
import {
  getProfile,
  saveProfile,
  setBehavior,
  completionSteps,
  completionPercent,
  type GameType,
  type GameBehavior,
  type AgentProfileData,
} from "./profile";

export interface MeResponse {
  agentId: string;
  agentName: string;
  displayName: string;
  bio: string;
  avatarUrl: string;
  verified: boolean;
  completionPercent: number;
  steps: { key: string; label: string; done: boolean }[];
  withdrawalConfigured: boolean;
}

/** GET /v1/me — agent identity + profile-completion snapshot. */
export async function getMe(session?: Session): Promise<MeResponse> {
  const hasSession = Boolean(session?.dashboardToken || session?.apiKey);
  const p = getProfile();
  const steps = completionSteps({ hasSession }).map((s) => ({ key: s.key, label: s.label, done: s.done }));
  // Best-effort backend enrichment of the agent name; never blocks the flow.
  let agentName = session?.agentName ?? "";
  try {
    if (session?.dashboardToken) {
      const r = await apiRequest<{ agent?: string; name?: string }>("/v1/me", { token: session.dashboardToken });
      agentName = r.name ?? r.agent ?? agentName;
    }
  } catch {
    /* fall back to client/session data */
  }
  return {
    agentId: session?.agentId ?? "",
    agentName,
    displayName: p.displayName,
    bio: p.bio,
    avatarUrl: p.avatar,
    verified: hasSession,
    completionPercent: completionPercent({ hasSession }),
    steps,
    withdrawalConfigured: p.withdrawalConfigured,
  };
}

/** POST /v1/agent/profile — update display name, bio, avatar (one agent / user). */
export async function updateAgentProfile(
  session: Session | undefined,
  patch: { displayName?: string; bio?: string; avatarUrl?: string },
): Promise<AgentProfileData> {
  try {
    if (session?.dashboardToken) {
      await apiRequest("/v1/agent/profile", {
        method: "POST",
        token: session.dashboardToken,
        body: {
          display_name: patch.displayName,
          bio: patch.bio,
          avatar_url: patch.avatarUrl,
        },
      });
    }
  } catch {
    /* persist locally regardless so the UI stays consistent */
  }
  return saveProfile({
    ...(patch.displayName !== undefined ? { displayName: patch.displayName } : {}),
    ...(patch.bio !== undefined ? { bio: patch.bio } : {}),
    ...(patch.avatarUrl !== undefined ? { avatar: patch.avatarUrl } : {}),
  });
}

/** POST /v1/agent/game-config — per-game behaviour for the owner's single agent. */
export async function setGameConfig(
  session: Session | undefined,
  game: GameType,
  behavior: GameBehavior,
): Promise<AgentProfileData> {
  try {
    if (session?.dashboardToken) {
      await apiRequest("/v1/agent/game-config", {
        method: "POST",
        token: session.dashboardToken,
        body: { game, behavior },
      });
    }
  } catch {
    /* local fallback */
  }
  return setBehavior(game, behavior);
}

export interface NotificationItem {
  id: string;
  kind: "profile" | "withdrawal" | "system";
  title: string;
  detail: string;
  href: string;
  cta: string;
}

/** GET /v1/notifications — currently derived from profile completion. */
export async function fetchNotifications(session?: Session): Promise<NotificationItem[]> {
  const hasSession = Boolean(session?.dashboardToken || session?.apiKey);
  try {
    if (session?.dashboardToken) {
      const r = await apiRequest<{ notifications: NotificationItem[] }>("/v1/notifications", {
        token: session.dashboardToken,
      });
      if (Array.isArray(r.notifications) && r.notifications.length) return r.notifications;
    }
  } catch {
    /* derive locally */
  }
  return completionSteps({ hasSession })
    .filter((s) => !s.done)
    .map((s) => ({
      id: `profile-${s.key}`,
      kind: s.key === "withdrawal" ? "withdrawal" : "profile",
      title: s.label,
      detail: "Complete this step to finish setting up your agent.",
      href: s.href,
      cta: s.cta,
    }));
}

export interface MagicLinkResult {
  sent: boolean;
  devToken?: string; // dev/offline fallback only — never returned by prod backend
}

/** POST /v1/auth/magic-link — email a one-time sign-in link (passwordless recovery). */
export async function requestMagicLink(email: string): Promise<MagicLinkResult> {
  try {
    await apiRequest("/v1/auth/magic-link", { method: "POST", body: { email } });
    return { sent: true };
  } catch {
    // Offline/demo fallback: hand back a dev token so the verify page is testable.
    const devToken = `dev-${Math.random().toString(36).slice(2, 10)}`;
    return { sent: true, devToken };
  }
}

/** GET /v1/auth/magic-link/verify — consume a link, returning a fresh session. */
export async function verifyMagicLink(
  token: string,
): Promise<{ dashboard_token: string; api_key?: string; agent_id?: string }> {
  try {
    return await apiRequest<{ dashboard_token: string; api_key?: string; agent_id?: string }>(
      `/v1/auth/magic-link/verify?token=${encodeURIComponent(token)}`,
    );
  } catch (err) {
    // Demo fallback: accept dev tokens so the recovery UX is exercisable offline.
    if (token.startsWith("dev-")) return { dashboard_token: token };
    throw err;
  }
}

// ── Monopoly (AI-vs-AI property strategy) ───────────────────────────────────

/** SSE endpoint for spectating a Monopoly match live (public). */
export function monopolyWatchUrl(id: string): string {
  return `${API_BASE}/v1/monopoly/${encodeURIComponent(id)}/watch`;
}

export interface MonopolyLiveMatch {
  matchId: string;
  title: string;
  teams: string[]; // real agent handles at the table (bots fill the rest)
  players?: number; // total seats
  active?: number; // solvent players remaining
  round: number;
  phase: string;
  leader?: string;
  winner?: number;
  watchers: number;
}

/** Live Monopoly tables. Empty array when the backend is offline (UI falls back
 *  to the local deterministic demo in lib/monopoly.ts). */
export async function fetchMonopolyLive(signal?: AbortSignal): Promise<MonopolyLiveMatch[]> {
  try {
    const data = await apiRequest<{ matches: any[] }>("/v1/monopoly/live", { signal });
    return (data.matches ?? []).map((m) => ({
      matchId: m.match_id,
      title: m.title,
      teams: m.agents ?? [],
      players: m.players,
      active: m.active,
      round: m.round,
      phase: m.phase,
      winner: m.winner,
      watchers: m.watchers,
    }));
  } catch {
    return [];
  }
}

// ── Monopoly economy, replay & agent-scope play ─────────────────────────────
// Monopoly is a perfect-information game except future randomness: the state
// returned here is the REDACTED engine state (future card decks stripped), so an
// agent can never read cards it hasn't drawn yet.

export interface MonopolyEconomy {
  agents: number;
  entryFee: number;
  grossPool: number;
  platformFeePct: number;
  platformFee: number;
  rewardPool: number;
}

export interface MonopolyReward {
  seat: number;
  agentId?: string;
  isWinner: boolean;
  eligible: boolean;
  payout: number;
  reason: string;
}

export interface MonopolyLogEvent {
  seq: number;
  type: string;
  payload: Record<string, unknown>;
}

/** One player's live state (redacted engine State, snake_case as the server emits). */
export interface MonoPlayerState {
  seat: number;
  cash: number;
  position: number;
  in_jail: boolean;
  jail_turns: number;
  jail_cards: number;
  bankrupt: boolean;
  doubles: number;
}

export interface MonoHoldingState {
  owner: number; // -1 (bank) or seat
  houses: number; // 0..5 (5 == hotel)
  mortgaged: boolean;
}

export interface MonoBoardState {
  players: MonoPlayerState[];
  holdings: MonoHoldingState[];
  current: number;
  phase: string;
  last_roll: [number, number];
  turn_count: number;
  finished: boolean;
  winner: number;
  auction?: { property: number; high_bid: number; high_bidder: number; current: number } | null;
  debt?: { debtor: number; creditor: number; amount: number; property: number; reason: string } | null;
}

export interface MonopolyAgentView {
  matchId: string;
  status: string; // active | finished | aborted
  yourSeat: number;
  turn: number; // seat the engine is waiting on
  yourTurn: boolean;
  phase: string;
  legal: string[];
  state: MonoBoardState | null;
  entryFee: number;
  economy: MonopolyEconomy;
  result?: { winnerSeat: number; rewards: MonopolyReward[] };
}

function mapMonopolyEconomy(e: any): MonopolyEconomy {
  return {
    agents: e?.agents ?? 0,
    entryFee: e?.entry_fee ?? 0,
    grossPool: e?.gross_pool ?? 0,
    platformFeePct: e?.platform_fee_pct ?? 0,
    platformFee: e?.platform_fee ?? 0,
    rewardPool: e?.reward_pool ?? 0,
  };
}

function mapMonopolyReward(r: any): MonopolyReward {
  return {
    seat: r?.seat ?? 0,
    agentId: r?.agent_id || undefined,
    isWinner: Boolean(r?.is_winner),
    eligible: Boolean(r?.eligible),
    payout: r?.payout ?? 0,
    reason: String(r?.reason ?? ""),
  };
}

function mapMonopolyView(v: any): MonopolyAgentView {
  return {
    matchId: v?.match_id ?? "",
    status: v?.status ?? "",
    yourSeat: v?.your_seat ?? 0,
    turn: v?.turn ?? -1,
    yourTurn: Boolean(v?.your_turn),
    phase: v?.phase ?? "",
    legal: v?.legal ?? [],
    state: (v?.state ?? null) as MonoBoardState | null,
    entryFee: v?.entry_fee ?? 0,
    economy: mapMonopolyEconomy(v?.economy ?? {}),
    result: v?.result
      ? { winnerSeat: v.result.winner_seat, rewards: (v.result.rewards ?? []).map(mapMonopolyReward) }
      : undefined,
  };
}

/** GET /v1/monopoly/{id}/economy — server pool breakdown (+ rewards once finished). */
export async function fetchMonopolyEconomy(
  id: string,
  signal?: AbortSignal,
): Promise<{ economy: MonopolyEconomy; rewards: MonopolyReward[] } | null> {
  try {
    const d = await apiRequest<{ economy: any; rewards: any[] }>(
      `/v1/monopoly/${encodeURIComponent(id)}/economy`,
      { signal },
    );
    return { economy: mapMonopolyEconomy(d.economy), rewards: (d.rewards ?? []).map(mapMonopolyReward) };
  } catch {
    return null;
  }
}

/** GET /v1/monopoly/{id}/replay — full public event log (deterministic replay). */
export async function fetchMonopolyReplay(id: string, signal?: AbortSignal): Promise<MonopolyLogEvent[]> {
  try {
    const d = await apiRequest<{ events: MonopolyLogEvent[] }>(
      `/v1/monopoly/${encodeURIComponent(id)}/replay`,
      { signal },
    );
    return d.events ?? [];
  } catch {
    return [];
  }
}

/** POST /v1/monopoly/lobby/create — open a table (creator seat 0 + server bots). */
export function monopolyCreateTable(
  session: Session,
  entryFee: number,
  players: number,
): Promise<{ match_id: string }> {
  return apiRequest("/v1/monopoly/lobby/create", {
    method: "POST",
    token: session.apiKey,
    body: { entry_fee: entryFee, players },
  });
}

/** GET /v1/monopoly/{id}/state — current redacted view for this seat (agent key). */
export async function fetchMonopolyState(session: Session, matchId: string): Promise<MonopolyAgentView> {
  const v = await apiRequest<any>(`/v1/monopoly/${encodeURIComponent(matchId)}/state`, {
    token: session.apiKey,
  });
  return mapMonopolyView(v);
}

export interface MonopolyActionInput {
  action: string; // roll | buy | decline | build | mortgage | unmortgage | sell_house | end_turn | pay_jail | roll_jail | use_jail_card | bid | pass | bankrupt
  property?: number;
  amount?: number;
}

/** POST /v1/monopoly/{id}/action — submit this seat's action (agent key). */
export async function monopolyAct(
  session: Session,
  matchId: string,
  act: MonopolyActionInput,
): Promise<MonopolyAgentView> {
  const v = await apiRequest<any>(`/v1/monopoly/${encodeURIComponent(matchId)}/action`, {
    method: "POST",
    token: session.apiKey,
    body: act,
  });
  return mapMonopolyView(v);
}
