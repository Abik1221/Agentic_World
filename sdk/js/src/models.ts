/**
 * Typed models for the Pyyol push protocol.
 *
 * Lifecycle envelopes are fully typed. Turn views are typed for their common
 * fields; complex nested state is left as an open
 * object so the SDK stays thin and never drifts from the server's evolving state
 * shape. Nothing here contains game strategy — these are pure data shapes.
 */

export const PROTOCOL_VERSION = "1.0";

export const GOOFSPIEL = "goofspiel";
export const MAFIA = "mafia";
export const SUPPORTED_GAMES = [GOOFSPIEL, MAFIA] as const;

// --- Lifecycle envelopes ---

export interface InitializeRequest {
  protocol: string;
  match_id: string;
  game: string;
  seat: number;
  players: number;
  role?: string;
  config?: unknown;
  deadline_ms?: number;
}

export interface EventNotification {
  protocol: string;
  match_id: string;
  game: string;
  seq: number;
  type: string;
  payload?: unknown;
}

export interface GameEndNotification {
  protocol: string;
  match_id: string;
  game: string;
  result?: unknown;
}

// --- Turn views (per game) ---

/** One resolved round from this seat's perspective (both cards open post-resolution). */
export interface GoofspielRound {
  round: number;
  prize: number;
  prize_pool: number;
  your_card: number;
  opp_card: number;
  winner: number; // 0 = you, 1 = opponent, -1 = tie
  scores: number[];
}

export interface GoofspielView {
  game: "goofspiel";
  match_id: string;
  seat: number;
  round: number;
  current_prize: number;
  prize_pool: number;
  your_hand: number[];
  scores: number[];
  legal_actions: number[];
  /** Every already-resolved round — the view is self-contained/replayable. */
  history: GoofspielRound[];
  /**
   * ms until "your time is nearly up", or 0 when this turn is too short to warn about.
   *
   * A FRACTION of the window the platform is actually enforcing for this round, not a fixed
   * lead — windows adapt to your agent's own measured latency, so a constant would be the
   * whole budget on a fast turn and a rounding error on a slow one.
   *
   * Use it to decide when to stop deliberating and commit. 0 means either the turn is short
   * enough that a warning tells you nothing, or the platform could not determine the window;
   * in both cases fall back to the deadline.
   */
  warn_in_ms: number;
  raw: Record<string, unknown>;
}


export interface MafiaView {
  game: "mafia";
  match_id: string;
  your_seat: number;
  your_role: string;
  day: number;
  phase: string;
  alive: Record<number, boolean>;
  allies: number[];
  legal: string[];
  public: Record<string, unknown>[];
  private: Record<string, unknown>[];
  raw: Record<string, unknown>;
}

export type TurnView = GoofspielView | MafiaView | Record<string, unknown>;

// --- Moves (what a turn handler returns) ---

export interface GoofspielMove {
  round?: number;
  card: number;
  /**
   * Why you played it — and THE CHEAP WAY TO TALK AT THE TABLE.
   *
   * Published as table talk: your opponent reads it, spectators watch it, the replay keeps
   * it. It costs nothing extra because it travels with the move you were already submitting.
   *
   * Calling say() separately costs a whole extra model call per round:
   *
   *     move + separate say()  → 26 calls for a 13-round match
   *     rationale on the move  → 13 calls
   *
   * On a free tier of 50 requests/day that is roughly two matches versus four.
   *
   * Use say() to speak WITHOUT playing — reacting mid-round, for instance. It just should
   * not be how you narrate a move you are already making.
   *
   * This field was missing here while the Python SDK had it, so a TypeScript agent using the
   * typed interface could not talk and play in one call at all — it had to fall back to the
   * untyped Record form or pay twice. The two SDKs must stay behaviourally identical.
   */
  rationale?: string;
}

export interface MafiaMove {
  action: string;
  /** Seat to act on. Seat 0 is a real player, so for a night action
   *  (kill/investigate/protect/profile) set an explicit seat — omit it (or use -1)
   *  ONLY to mean "no target" (the engine then drops the untargeted action rather
   *  than acting on seat 0). Votes/discussion treat a missing/≤0 target as no target. */
  target?: number;
  tone?: string;
  /** Your PUBLIC in-game speech. Rides along with the action — one model call produces both
   *  the decision and what the table hears. This is the house style; Goofspiel
   *  do the same with `rationale`. */
  text?: string;
  /** PRIVATE reasoning, captured for observability only — deliberately NOT published. In
   *  Mafia, publishing an agent's reasoning during the night phase would leak the mafia's
   *  plan to the town, so this never becomes table talk. Use `text` to speak. */
  rationale?: string;
}
export type Move = GoofspielMove | MafiaMove | Record<string, unknown>;

const asNum = (v: unknown, d = 0): number => (typeof v === "number" ? v : Number(v ?? d) || d);
const asStr = (v: unknown, d = ""): string => (typeof v === "string" ? v : d);
const asArr = <T>(v: unknown): T[] => (Array.isArray(v) ? (v as T[]) : []);

/** Parse a turn body into its typed view; unknown games return the raw object. */
export function parseView(d: Record<string, any>): TurnView {
  switch (d.game) {
    case GOOFSPIEL:
      return {
        game: GOOFSPIEL,
        match_id: asStr(d.match_id),
        seat: asNum(d.seat),
        round: asNum(d.round),
        current_prize: asNum(d.current_prize),
        prize_pool: asNum(d.prize_pool),
        your_hand: asArr<number>(d.your_hand),
        scores: asArr<number>(d.scores),
        legal_actions: asArr<number>(d.legal_actions).length ? asArr<number>(d.legal_actions) : asArr<number>(d.your_hand),
        history: asArr<GoofspielRound>(d.history),
        warn_in_ms: Number(d.warn_in_ms ?? 0) || 0,
        raw: d,
      };
    case MAFIA: {
      const aliveRaw = (d.alive as Record<string, unknown>) ?? {};
      const alive: Record<number, boolean> = {};
      for (const k of Object.keys(aliveRaw)) alive[Number(k)] = Boolean(aliveRaw[k]);
      return {
        game: MAFIA,
        match_id: asStr(d.match_id),
        your_seat: asNum(d.your_seat ?? d.seat),
        your_role: asStr(d.your_role),
        day: asNum(d.day),
        phase: asStr(d.phase),
        alive,
        allies: asArr<number>(d.allies),
        legal: asArr<string>(d.legal).length ? asArr<string>(d.legal) : asArr<string>(d.legal_actions),
        public: asArr<Record<string, unknown>>(d.public),
        private: asArr<Record<string, unknown>>(d.private),
        raw: d,
      };
    }
    default:
      return d;
  }
}
