#!/usr/bin/env bash
# Run one model-vs-model pairing, end to end, and tear it down.
#
#   ./run_pairing.sh <game> <modelA> <modelB> [matches]
#   ./run_pairing.sh goofspiel opus-5 gpt-5.6-sol 30
#
# What this does, in order:
#   1. brings up one bench container per seat, each pinned to ONE model;
#   2. waits for every seat to answer /health, because onboarding's verification probe will
#      call it and a probe that arrives before the process is listening fails the whole run
#      with "endpoint not verified", which reads as a platform fault and is not one;
#   3. runs gamelab with -harness (admin onboarding as kind='harness', so results land on
#      the platform benchmark and NOT on the public developer leaderboard) and -agent-urls
#      pointing at those containers;
#   4. tears the containers down, leaving the ledgers behind in the state volume.
#
# The budget file is shared by every container through the same state directory, so the
# ceiling is a property of the RUN rather than of a container. Launching this script twice
# concurrently is safe for the money and unsafe for nothing else.
set -euo pipefail

GAME="${1:?usage: run_pairing.sh <game> <modelA> <modelB> [matches]}"
MODEL_A="${2:?model A}"
MODEL_B="${3:?model B}"
MATCHES="${4:-20}"

: "${OPENROUTER_API_KEY:?export OPENROUTER_API_KEY first}"

RUN_LABEL="${RUN_LABEL:-$(date +%Y%m%d-%H%M%S)}"
NET="${PYYOL_NET:-pyyol-lab}"
STATE_DIR="${PYYOLBENCH_STATE_DIR:-$PWD/state}"
BUDGET="${PYYOLBENCH_BUDGET_USD:-10.00}"
API_BASE="${API_BASE:-http://pyyol-backend:8080}"
IMAGE="${BENCH_IMAGE:-pyyol-bench:latest}"

# Seats per game. Mafia is twelve, and twelve frontier models answering every phase is the
# most expensive thing this harness can do — the run plan enters it last and with the cheap
# models, never as an exploratory first run.
case "$GAME" in
  goofspiel) SEATS=2 ;;
  monopoly)  SEATS=4 ;;
  mafia)     SEATS=12 ;;
  *) echo "unknown game: $GAME" >&2; exit 2 ;;
esac

mkdir -p "$STATE_DIR"
CONTAINERS=()
cleanup() {
  # Always runs, including on Ctrl-C. Ledgers live in the mounted state dir, so removing the
  # containers loses nothing that matters.
  if [ ${#CONTAINERS[@]} -gt 0 ]; then
    echo "── tearing down ${#CONTAINERS[@]} agent container(s)"
    docker rm -f "${CONTAINERS[@]}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

echo "── run $RUN_LABEL: $GAME, $SEATS seats, ${MATCHES} matches, budget ceiling \$$BUDGET"
echo "   seat models alternate: $MODEL_A / $MODEL_B"

URLS=""
for i in $(seq 0 $((SEATS - 1))); do
  # Alternate the two models across seats. Alternating rather than blocking (A,A,B,B) so
  # that in Goofspiel the two models always face each other, and in the bigger games neither
  # model is concentrated in the seats that move first.
  if [ $((i % 2)) -eq 0 ]; then MODEL="$MODEL_A"; else MODEL="$MODEL_B"; fi
  NAME="bench-${RUN_LABEL}-${GAME}-s${i}"
  PORT=$((9101 + i))

  docker run -d --rm \
    --name "$NAME" \
    --network "$NET" \
    -e PYYOLBENCH_MODEL="$MODEL" \
    -e PYYOLBENCH_GAME="$GAME" \
    -e PYYOLBENCH_SEAT="$i" \
    -e PYYOLBENCH_PORT="$PORT" \
    -e PYYOLBENCH_RUN="$RUN_LABEL" \
    -e PYYOLBENCH_BUDGET_USD="$BUDGET" \
    -e OPENROUTER_API_KEY="$OPENROUTER_API_KEY" \
    -e PYYOL_GATEWAY_BASE="${PYYOL_GATEWAY_BASE:-$API_BASE/v1}" \
    -v "$STATE_DIR":/state \
    "$IMAGE" >/dev/null

  CONTAINERS+=("$NAME")
  [ -n "$URLS" ] && URLS="$URLS,"
  URLS="$URLS http://$NAME:$PORT"
  URLS="${URLS// /}"
  echo "   seat $i → $MODEL  ($NAME:$PORT)"
done

# Wait for every seat before onboarding. The verification probe is unforgiving and the
# failure it produces names the platform rather than the agent, so this loop is what keeps a
# slow Python start from looking like a server bug.
echo "── waiting for seats to answer /health"
for i in $(seq 0 $((SEATS - 1))); do
  NAME="bench-${RUN_LABEL}-${GAME}-s${i}"
  PORT=$((9101 + i))
  for attempt in $(seq 1 60); do
    if docker run --rm --network "$NET" curlimages/curl:latest \
        -fsS --max-time 3 "http://$NAME:$PORT/health" >/dev/null 2>&1; then
      break
    fi
    if [ "$attempt" -eq 60 ]; then
      echo "FATAL: seat $i never became healthy; its logs:" >&2
      docker logs "$NAME" >&2 || true
      exit 1
    fi
    sleep 1
  done
done
echo "   all $SEATS seats healthy"

# gamelab does onboarding, funding, seating and batching. -bind is deliberately NOT passed:
# the bench agents make their own gateway calls through the SDK, and -bind would have
# gamelab make a second, competing call for the same turn.
echo "── running $MATCHES matches through gamelab (harness mode)"
docker run --rm \
  --network "$NET" \
  -e API_BASE="$API_BASE" \
  -e PYYOL_HARNESS=true \
  -e PYYOL_PLATFORM_SEED="${PYYOL_PLATFORM_SEED:-}" \
  -v "$PWD/../..":/r \
  -v pyyol-gocache:/root/.cache/go-build \
  -v pyyol-gomod:/go/pkg/mod \
  -w /r/backend golang:1.25-alpine \
  go run ./cmd/gamelab \
    -game "$GAME" \
    -harness \
    -matches "$MATCHES" \
    -label "$RUN_LABEL" \
    -agent-urls "$URLS"

echo "── run $RUN_LABEL complete"
echo "   ledgers: $STATE_DIR/ledger/$RUN_LABEL/"
python3 - "$STATE_DIR/budget.json" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1]))
except Exception:
    sys.exit(0)
print(f"   spend: ${d.get('settled_usd', 0):.4f} of ${d.get('limit_usd', 0):.2f} "
      f"across {d.get('calls', 0)} calls")
for k, v in sorted(d.get("by_model", {}).items()):
    print(f"     {k:<20} ${v:.4f}")
PY
