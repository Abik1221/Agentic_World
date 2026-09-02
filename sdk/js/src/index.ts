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
export { Adapter, asAgent } from "./adapter.js";
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
export { gameRules } from "./rules.js";
export { Tracer, Span, currentSpan, matchTraceId, currentUsage, UsageAccumulator, runTurnUsage } from "./telemetry.js";
export type { TracerOptions, ModelCall, MoveUsage, UsageAdd } from "./telemetry.js";
export { instrument, uninstrument, recordResponse, extractUsage, patchPrototype } from "./instrument.js";
export { route, enableGateway, disableGateway, gatewayBaseUrl, gatewayHeaders } from "./instrument.js";
export type { ExtractedUsage } from "./instrument.js";
// cacheWriteRate was defined and used internally but never re-exported, so "what will a cache
// write cost me?" was answerable in Python and not here. Cache writes bill at a premium, which
// makes it exactly the rate a developer wants to check before enabling caching.
export {
  estimateCost,
  rateFor,
  isKnown,
  canonical,
  cacheWriteRate,
  PRICING_VERSION,
} from "./pricing.js";
// Structured move tools: how an agent proves its MODEL chose the move it played. Routing
// through the gateway proves a call happened for a turn; a tool call is what proves the
// model's answer became the move. See src/movetools.ts.
export {
  moveTool,
  moveToolChoice,
  moveToolName,
  moveFromResponse,
  boundMove,
  // Range bindings: one completion that decided several rounds. Coverage counts DECISIONS a
  // model made, not calls, so batching no longer costs an agent its verified share.
  boundPlan,
  canonPlan,
  // Renders a turn view as a prompt the move tools expect. Parity with Python's prompt_for.
  promptFor,
  PLAN_KEY,
  MAX_SPAN_ROUNDS,
  canonMove,
  canonGoofspiel,
  canonMafia,
  NO_TARGET,
  TOOL_GOOFSPIEL,
  TOOL_MAFIA,
  GAME_GOOFSPIEL,
  GAME_MAFIA,
} from "./movetools.js";
export type { Rate, CostArgs } from "./pricing.js";
// Scaffold fingerprinting: the harness identity that makes a paired model comparison
// possible (same scaffold, different model). Exported so a developer can print their own
// fingerprint and confirm it is stable before relying on it.
export {
  fingerprint as scaffoldFingerprint,
  fromRequest as scaffoldFromRequest,
  eligibleForPairing as scaffoldEligibleForPairing,
  SCAFFOLD_VERSION,
} from "./scaffold.js";
