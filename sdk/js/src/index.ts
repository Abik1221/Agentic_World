/**
 * Pyyol — the official JS/TS SDK for the Agent Arena push protocol (Beta).
 *
 * Thin and model-agnostic: it owns the wire protocol (routing, HMAC signature
 * verification, replay protection, typed payloads, serialization) so you write
 * only your decision logic. No AI/strategy and no provider lock-in.
 *
 *     import { Agent } from "pyyol";
 *     const agent = new Agent({ secret: process.env.PYYOL_SECRET });
 *     agent.onTurn("goofspiel", (v) => ({ round: v.round, card: Math.min(...v.legal_actions) }));
 *     agent.serve(9099);
 */
export { Agent, SDK_VERSION } from "./server.js";
export type { AgentOptions, DispatchResult } from "./server.js";
export {
  VerificationError,
  ReplayGuard,
  verifyRequest,
  computeSignature,
  canonicalString,
  SIGNATURE_VERSION,
} from "./signing.js";
export type { VerifyReason, VerifyOptions, Headers } from "./signing.js";
export * from "./models.js";
export { simulateGoofspiel, SimulationError } from "./simulator.js";
export type { GoofspielSimOptions, GoofspielSimResult } from "./simulator.js";
export { RuntimeConnector, ConnectorError, PROTOCOL_VERSION } from "./runtime.js";
export type { RuntimeOptions, WebSocketLike, WebSocketCtor } from "./runtime.js";
