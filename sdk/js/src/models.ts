/**
 * Typed models for the Pyyol push protocol.
 *
 * Lifecycle envelopes are fully typed. Turn views are typed for their common
 * fields; complex nested state (e.g. the Monopoly board) is left as an open
 * object so the SDK stays thin and never drifts from the server's evolving state
 * shape. Nothing here contains game strategy — these are pure data shapes.
 */

export const PROTOCOL_VERSION = "1.0";

export const GOOFSPIEL = "goofspiel";
export const MONOPOLY = "monopoly";
export const MAFIA = "mafia";
export const SUPPORTED_GAMES = [GOOFSPIEL, MONOPOLY, MAFIA] as const;

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
  raw: Record<string, unknown>;
}

export interface MonopolyView {
  game: "monopoly";
  match_id: string;
  seat: number;
  phase: string;
  legal_actions: string[];
  /** The raw board dict (players, holdings, phase, …) — inspect directly. */
  state: Record<string, unknown>;
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

export type TurnView = GoofspielView | MonopolyView | MafiaView | Record<string, unknown>;

// --- Moves (what a turn handler returns) ---

export interface GoofspielMove {
  round?: number;
  card: number;
}
export interface MonopolyMove {
  action: string;
  property?: number;
  amount?: number;
}
export interface MafiaMove {
  action: string;
  target?: number;
  tone?: string;
  text?: string;
}
export type Move = GoofspielMove | MonopolyMove | MafiaMove | Record<string, unknown>;

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
        raw: d,
      };
    case MONOPOLY:
      return {
        game: MONOPOLY,
        match_id: asStr(d.match_id),
        seat: asNum(d.seat),
        phase: asStr(d.phase),
        legal_actions: asArr<string>(d.legal_actions),
        state: (d.state as Record<string, unknown>) ?? {},
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
