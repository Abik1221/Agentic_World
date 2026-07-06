/**
 * Local simulation harness — test your agent without the platform.
 *
 * `simulateGoofspiel` runs a complete Goofspiel match in-process against a
 * baseline opponent, driving your agent through its *real* dispatch path
 * (`Agent.handle` with valid signatures) — validating routing, signature
 * verification, parsing, and your handlers end to end, then reporting the result.
 * It contains Goofspiel rules only (to referee); your handler decides every move.
 */
import type { Agent } from "./server.js";
import { GOOFSPIEL, PROTOCOL_VERSION } from "./models.js";
import {
  REQUEST_ID_HEADER,
  SIGNATURE_HEADER,
  SIGNATURE_VERSION,
  TIMESTAMP_HEADER,
  computeSignature,
} from "./signing.js";

export class SimulationError extends Error {}

function signedHeaders(secret: string, method: string, path: string, body: Buffer, nonce: string, ts: string) {
  const h: Record<string, string> = {
    [TIMESTAMP_HEADER]: ts,
    [REQUEST_ID_HEADER]: nonce,
    "content-type": "application/json",
  };
  if (secret) {
    h[SIGNATURE_HEADER] = `${SIGNATURE_VERSION}=${computeSignature(secret, ts, nonce, method, path, body)}`;
    h["authorization"] = `Bearer ${secret}`;
  }
  return h;
}

async function post(agent: Agent, path: string, payload: unknown, seq: number): Promise<Record<string, any>> {
  const body = Buffer.from(JSON.stringify(payload));
  const nonce = `sim_${seq}`;
  const ts = new Date(Date.now()).toISOString().replace(/\.\d+Z$/, "Z");
  const headers = signedHeaders(agent.secret, "POST", path, body, nonce, ts);
  const { status, body: resp } = await agent.handle("POST", path, headers, body);
  if (status !== 200) throw new SimulationError(`agent returned ${status} for ${path}: ${JSON.stringify(resp)}`);
  return resp;
}

export interface GoofspielSimOptions {
  handSize?: number;
  seed?: number;
  shufflePrizes?: boolean;
  turnPath?: string;
}

export interface GoofspielSimResult {
  winner: "agent" | "baseline" | "tie";
  winnerSeat: number;
  scores: { agent: number; baseline: number };
  rounds: number;
  moves: { round: number; prize: number; pool: number; dev: number; opp: number }[];
}

/** Play one Goofspiel match: your agent (seat 0) vs a baseline (seat 1). Throws
 *  {@link SimulationError} if your agent returns an illegal or malformed move. */
export async function simulateGoofspiel(agent: Agent, opts: GoofspielSimOptions = {}): Promise<GoofspielSimResult> {
  const handSize = opts.handSize ?? 13;
  const turnPath = opts.turnPath ?? "/turn";
  const rng = mulberry32(opts.seed ?? 1);

  const prizes = Array.from({ length: handSize }, (_, i) => i + 1);
  if (opts.shufflePrizes) shuffle(prizes, rng);

  const devHand = Array.from({ length: handSize }, (_, i) => i + 1);
  const oppHand = Array.from({ length: handSize }, (_, i) => i + 1);
  const scores = [0, 0];
  let carried = 0;
  const moves: GoofspielSimResult["moves"] = [];
  let seq = 0;

  await post(agent, "/initialize", { protocol: PROTOCOL_VERSION, match_id: "sim-goofspiel", game: GOOFSPIEL, seat: 0, players: 2 }, seq++);

  for (let round = 0; round < prizes.length; round++) {
    const prize = prizes[round];
    const pool = prize + carried;
    const view = {
      game: GOOFSPIEL, match_id: "sim-goofspiel", seat: 0, round,
      current_prize: prize, prize_pool: pool,
      your_hand: [...devHand], scores: [...scores], legal_actions: [...devHand],
    };
    const resp = await post(agent, turnPath, view, seq++);
    const devCard = resp.card;
    if (!devHand.includes(devCard)) {
      throw new SimulationError(`round ${round}: agent bid ${devCard}, not in hand [${devHand}]`);
    }
    const oppCard = baselineBid(oppHand, pool);
    devHand.splice(devHand.indexOf(devCard), 1);
    oppHand.splice(oppHand.indexOf(oppCard), 1);
    moves.push({ round, prize, pool, dev: devCard, opp: oppCard });

    if (devCard > oppCard) { scores[0] += pool; carried = 0; }
    else if (oppCard > devCard) { scores[1] += pool; carried = 0; }
    else carried = pool;

    await post(agent, "/event", {
      protocol: PROTOCOL_VERSION, match_id: "sim-goofspiel", game: GOOFSPIEL,
      seq: round, type: "round_revealed", payload: { prize, dev_card: devCard, opp_card: oppCard },
    }, seq++);
  }

  const winnerSeat = scores[0] > scores[1] ? 0 : scores[1] > scores[0] ? 1 : -1;
  await post(agent, "/game-end", {
    protocol: PROTOCOL_VERSION, match_id: "sim-goofspiel", game: GOOFSPIEL,
    result: { winner_seat: winnerSeat, scores },
  }, seq++);

  return {
    winner: winnerSeat === 0 ? "agent" : winnerSeat === 1 ? "baseline" : "tie",
    winnerSeat,
    scores: { agent: scores[0], baseline: scores[1] },
    rounds: prizes.length,
    moves,
  };
}

/** Deterministic opponent: bid the card nearest the pool value (ties -> lower). */
function baselineBid(hand: number[], pool: number): number {
  return hand.reduce((best, c) => (Math.abs(c - pool) < Math.abs(best - pool) ? c : best), hand[0]);
}

// Small deterministic RNG so simulations are reproducible from a seed.
function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a |= 0; a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
function shuffle<T>(arr: T[], rng: () => number): void {
  for (let i = arr.length - 1; i > 0; i--) {
    const j = Math.floor(rng() * (i + 1));
    [arr[i], arr[j]] = [arr[j], arr[i]];
  }
}
