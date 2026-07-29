#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/.."

COMPOSE=(docker compose -f bench/docker-compose.yml)
RESULTS_DIR="bench/results"
DURATION="10s"
REPEAT=3
QUICK=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --quick)    QUICK=1; shift ;;
        --duration) DURATION="$2"; shift 2 ;;
        --repeat)   REPEAT="$2"; shift 2 ;;
        -h|--help)  sed -n '2,12p' "$0"; exit 0 ;;
        *)          echo "unknown flag: $1" >&2; exit 2 ;;
    esac
done

mkdir -p "$RESULTS_DIR"

if [[ $QUICK -eq 1 ]]; then
    SCENARIOS=("lan|||")
else
    SCENARIOS=(
        "lan|||"
        "rtt-50ms|25ms||"
        "rtt-100ms|50ms||"
        "rtt-200ms|100ms||"
        "loss-1pct|25ms|1|"
        "loss-3pct|25ms|3|"
    )
fi

log() { printf '\033[36m==>\033[0m %s\n' "$*" >&2; }

teardown() {
    log "tearing down"
    "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
}
trap teardown EXIT

log "building images (frp is fetched from its GitHub release)"

"${COMPOSE[@]}" --profile driver build >/dev/null

run_case() {
    local scenario="$1" stack="$2" target="$3" mode="$4" extra="$5" rep="$6"
    local out="$RESULTS_DIR/${scenario}-${stack}-${mode}-rep${rep}.json"

    if "${COMPOSE[@]}" run --rm --no-deps -T loadgen \
        -mode "$mode" -target "$target" -label "${stack}/${scenario}" \
        -duration "$DURATION" $extra 2>/dev/null \
        | sed -n '/^[[:space:]]*{/,$p' > "$out" && [ -s "$out" ]; then
        :
    else
        echo '{"error":"run failed"}' > "$out"
    fi
    cat "$out"
}

for entry in "${SCENARIOS[@]}"; do
    IFS='|' read -r scenario delay loss _ <<< "$entry"

    log "scenario ${scenario} (delay=${delay:-none} loss=${loss:-none})"

    NETEM_DELAY="$delay" NETEM_LOSS="$loss" \
        "${COMPOSE[@]}" up -d --force-recreate \
        openfrps openfrpc frps frpc >/dev/null

    sleep 8

    for stack in openfrp frp; do
        if [[ $stack == openfrp ]]; then target="openfrps:6000"; else target="frps:6000"; fi

        for rep in $(seq 1 "$REPEAT"); do
            log "  ${stack}: throughput (${rep}/${REPEAT})"
            run_case "$scenario" "$stack" "$target" throughput "-chunk 262144" "$rep" >/dev/null

            log "  ${stack}: latency (${rep}/${REPEAT})"
            run_case "$scenario" "$stack" "$target" latency "-concurrency 32 -payload 64" "$rep" >/dev/null
        done
    done
done

log "rendering results"
python3 bench/render.py "$RESULTS_DIR" | tee "$RESULTS_DIR/summary.md"
