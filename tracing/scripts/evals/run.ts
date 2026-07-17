/**
 * Run one or all Phase 10 evals (requires pyyol-lens query-api and env: PYYOL_LENS_ORG_ID, RUN_ID).
 * Usage: `npm run eval -- all` | `npm run eval -- lead` | manual import
 */
import { run as runLead } from "./eval-lead-context-size";
import { run as runCitation } from "./eval-citation-coverage";
import { run as runNeedle } from "./eval-needle-recovery";
import { run as runReducer } from "./eval-reducer-required-dimensions";
import { run as runSubagent } from "./eval-subagent-isolation";

const byName: Record<string, () => Promise<void>> = {
  "eval-lead-context-size": runLead,
  "eval-citation-coverage": runCitation,
  "eval-needle-recovery": runNeedle,
  "eval-reducer-required-dimensions": runReducer,
  "eval-subagent-isolation": runSubagent,
  lead: runLead,
  citation: runCitation,
  needle: runNeedle,
  reducer: runReducer,
  subagent: runSubagent,
};

async function main() {
  const arg = process.argv[2] || "all";
  if (arg === "all" || arg === "phase10") {
    for (const name of [
      "eval-lead-context-size",
      "eval-citation-coverage",
      "eval-needle-recovery",
      "eval-reducer-required-dimensions",
      "eval-subagent-isolation",
    ]) {
      console.log(`\n--- ${name} ---`);
      await byName[name]();
    }
    console.log("\nAll Phase 10 evals passed.");
    return;
  }
  const fn = byName[arg];
  if (!fn) {
    console.error("Usage: run.ts [all|phase10|lead|citation|needle|reducer|subagent|<file-name>]");
    process.exit(1);
  }
  await fn();
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
