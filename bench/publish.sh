#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/.."

RESULTS="bench/results/summary.md"
DOC="bench/results.md"
START="<!-- BENCH_RESULTS_START -->"
END="<!-- BENCH_RESULTS_END -->"

if [[ ! -f $RESULTS ]]; then
    echo "no results at $RESULTS; run ./bench/run.sh first" >&2
    exit 1
fi
if ! grep -qF "$START" "$DOC" || ! grep -qF "$END" "$DOC"; then
    echo "$DOC is missing its result markers" >&2
    exit 1
fi

python3 - "$RESULTS" "$DOC" "$START" "$END" <<'PY'
import sys

results_path, doc_path, start, end = sys.argv[1:5]

results = open(results_path).read().strip()
doc = open(doc_path).read()

head, _, rest = doc.partition(start)
_, _, tail = rest.partition(end)

open(doc_path, "w").write(f"{head}{start}\n\n{results}\n\n{end}{tail}")
PY

echo "published $RESULTS into $DOC" >&2
