/**
 * A complete Goofspiel agent. Build the SDK (`npm run build`), then:
 *
 *     PYYOL_SECRET=... node --loader ts-node/esm examples/goofspiel-agent.ts
 *     # or compile and run dist/
 *
 * Point your manifest `endpoint.url` at the /turn route (http://<host>:9099/turn);
 * /health, /initialize, /event and /game-end are served as siblings automatically.
 */
import { Agent } from "../src/index.js";
import type { GoofspielView } from "../src/index.js";

const agent = new Agent({
  secret: process.env.PYYOL_SECRET ?? "",
  supportedGames: ["goofspiel"],
  name: "lowball",
});

agent.onTurn("goofspiel", (view) => {
  const v = view as GoofspielView;
  // Spend the smallest legal card first — a simple, deterministic baseline.
  return { round: v.round, card: Math.min(...v.legal_actions) };
});

agent.onInitialize((req) => console.log(`match ${req.match_id} starting: seat=${req.seat} players=${req.players}`));
agent.onGameEnd((res) => console.log(`match ${res.match_id} finished:`, res.result));

agent.serve(Number(process.env.PORT ?? 9099));
