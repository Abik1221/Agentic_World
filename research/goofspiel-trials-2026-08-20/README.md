# The Goofspiel Trials — 2026-08-20

Five frontier models played Goofspiel head-to-head under one identical agent, with every
decision cryptographically bound to the model's own tool call.

**Published page:** https://claude.ai/code/artifact/29bfb7b0-6fe1-45a6-bab7-9bf5cea47dbe

## The headline is a retraction

At 13 matches this run showed a clean non-transitive cycle. Five more matches dissolved it.
Four conclusions reversed on a 38% increase in sample:

| claim at 13 matches | at 18 matches |
|---|---|
| DeepSeek beats Gemini 2–0 | Gemini leads 2–1 (157:107) |
| Gemini beats Opus-5 79:12 | split 1–1 (Opus won the replay 58:33) |
| Opus-5 last, differential −132 | third, differential −5 |
| Sonnet-5 beats DeepSeek | split 1–1 (83:98) |

Nothing was wrong with the measurement — every match completed and every decision bound. The
claims were built on pairings decided by a single game, and one game of Goofspiel does not
identify a better player. GTBench runs 50 matches per pairing; this run managed one to three.

**Do not cite the standings below as a ranking.** They are a pilot.

## Standings (18 matches, all 10 pairings)

| model | n | W | win% | 95% CI | pts | conceded | differential |
|---|---|---|---|---|---|---|---|
| gemini-3.7-flash | 9 | 5 | 56% | 27–81% | 516 | 294 | +222 |
| gpt-5.6-sol-pro | 4 | 3 | 75% | 30–95% | 220 | 144 | +76 |
| claude-opus-5 | 7 | 4 | 57% | 25–84% | 316 | 321 | −5 |
| deepseek-v4-pro | 8 | 3 | 38% | 14–69% | 294 | 424 | −130 |
| claude-sonnet-5 | 8 | 3 | 38% | 14–69% | 282 | 445 | −163 |

Every Wilson interval overlaps every other. No ordering is statistically separable.

## Server-observed economics (419 decisions, 100% bound)

| model | decisions | $/decision | p50 | p95 | reasoning tok |
|---|---|---|---|---|---|
| gemini-3.7-flash | 101 | $0.00131 | 5.5s | 11.4s | 45,343 |
| deepseek-v4-pro | 94 | $0.00226 | 1.3s | 18.2s | 2,378 |
| claude-sonnet-5 | 94 | $0.00450 | 3.3s | 4.2s | 0 |
| claude-opus-5 | 83 | $0.01068 | 3.2s | 5.0s | 0 |
| gpt-5.6-sol-pro | 47 | $0.01578 | 4.2s | 25.7s | 4,851 |

Cost computed from gateway-observed tokens at published rates: $2.3965. Total account spend
including pre-flight probes and a discarded pilot: $3.22.

## Negative findings

- **`deepseek/deepseek-v4-pro-0813` cannot emit a tool call.** Spent all 4,096 reasoning tokens
  (81s), then all 8,192 (157s), never calling the tool. Excluded; `deepseek-v4-pro` stands in.
- **GPT-5.6 bills ~3× the input** for a byte-identical prompt: 5,579 (`sol-pro`) / 5,812
  (`terra-pro`) against 1,851 (`claude-opus-5`) and 1,297 (`deepseek-v4-pro`).

## What binding does and does not prove

Binding proves the submitted move came from the call the gateway made. It does **not** prove the
provider served the advertised model — a substituted or quantized model would bind identically.
On provider substitution this benchmark is exactly as exposed as every other.

## Files

- `matches.json` — every completed match: seats, model per seat, final score, coins.
- `usage.json` — per-model server-observed tokens, latency percentiles, bind counts.

Both are exported from `agent_model_calls` and `match_players`, the gateway's own records.
`agent_match_decisions.model` is the agent's SELF-DECLARED string and must not be used for
model attribution.

## Reproducing

```bash
docker exec -e API_BASE=http://localhost:8080 -e AGENT_HOST=localhost \
  -e PYYOL_PROVIDER_KEY="$PYYOL_PROVIDER_KEY" \
  pyyol-backend /src/.lab-gamelab -game goofspiel -tier low -bind \
  -bind-provider openrouter -bind-model "<modelA>,<modelB>" -base-port 9101 -label rr
```

The key comes from the environment. It is never committed.
