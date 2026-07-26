#!/usr/bin/env python3
"""Render benchmark JSON into a Markdown comparison.

Each measurement is repeated, and the MEDIAN is reported with the observed
spread alongside it. A single sample on a shared machine is not stable enough
to compare two tunnels: back-to-back single-sample runs of this matrix
disagreed by 4x on one scenario, which is larger than the effect being
measured. Showing the spread lets a reader see which rows are solid and which
are noise.

Every scenario is printed, including the ones OpenFrp loses. A benchmark that
only shows wins is marketing, and the cases where multiplexing is genuinely
competitive are exactly what a reader needs in order to judge the defaults.
"""

import json
import pathlib
import re
import statistics
import sys

SCENARIO_ORDER = [
    "lan",
    "rtt-50ms",
    "rtt-100ms",
    "rtt-200ms",
    "loss-1pct",
    "loss-3pct",
]

SCENARIO_LABEL = {
    "lan": "LAN (no shaping)",
    "rtt-50ms": "50 ms delay",
    "rtt-100ms": "100 ms delay",
    "rtt-200ms": "200 ms delay",
    "loss-1pct": "50 ms delay, 1% loss",
    "loss-3pct": "50 ms delay, 3% loss",
}

# <scenario>-<stack>-<mode>-rep<N>.json, with the older unrepeated form
# tolerated so a stale results directory does not crash the renderer.
NAME = re.compile(r"^(?P<scenario>.+)-(?P<stack>openfrp|frp)-(?P<mode>throughput|latency)(?:-rep(?P<rep>\d+))?$")


def load(results_dir: pathlib.Path) -> dict:
    """Return {scenario: {stack: {mode: [sample, ...]}}}."""
    data: dict = {}
    for path in sorted(results_dir.glob("*.json")):
        match = NAME.match(path.stem)
        if not match:
            continue
        try:
            payload = json.loads(path.read_text())
        except (json.JSONDecodeError, OSError):
            continue
        bucket = (
            data.setdefault(match["scenario"], {})
            .setdefault(match["stack"], {})
            .setdefault(match["mode"], [])
        )
        bucket.append(payload)
    return data


def samples(runs: list, key: str) -> list:
    return [r[key] for r in runs if isinstance(r.get(key), (int, float))]


def median(runs: list, key: str):
    values = samples(runs, key)
    return statistics.median(values) if values else None


def spread(runs: list, key: str) -> str:
    """Report min-max when repetitions disagree meaningfully."""
    values = samples(runs, key)
    if len(values) < 2:
        return ""
    low, high = min(values), max(values)
    if low <= 0 or high / low < 1.15:
        return ""
    return f"<br><sub>{low:g}–{high:g}</sub>"


def cell(runs: list, key: str, unit: str = "") -> str:
    value = median(runs, key)
    if value is None:
        return "—"
    return f"{value:g}{unit}{spread(runs, key)}"


def ratio(runs_ours: list, runs_theirs: list, key: str) -> str:
    ours, theirs = median(runs_ours, key), median(runs_theirs, key)
    if not ours or not theirs:
        return "—"
    factor = ours / theirs
    return f"**{factor:.2f}×**" if factor >= 1 else f"{factor:.2f}×"


def get(data: dict, scenario: str, stack: str, mode: str) -> list:
    return data.get(scenario, {}).get(stack, {}).get(mode, [])


def main() -> int:
    results_dir = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "bench/results")
    data = load(results_dir)
    if not data:
        print("No results found. Run ./bench/run.sh first.", file=sys.stderr)
        return 1

    scenarios = [s for s in SCENARIO_ORDER if s in data]
    scenarios += sorted(s for s in data if s not in SCENARIO_ORDER)

    reps = max(
        (len(get(data, s, stack, mode))
         for s in scenarios for stack in ("openfrp", "frp")
         for mode in ("throughput", "latency")),
        default=0,
    )

    out = [
        f"Median of {reps} runs per measurement. Where repetitions disagreed by "
        "more than 15%, the observed range is shown beneath the median.\n",
        "### Single-stream throughput\n",
        "Higher is better. One connection, so there is no head-of-line blocking",
        "to suffer; this isolates the cost of moving bytes through userspace",
        "versus splicing them in the kernel.\n",
        "| Scenario | OpenFrp | frp | Ratio |",
        "|---|---:|---:|---:|",
    ]

    for scenario in scenarios:
        ours = get(data, scenario, "openfrp", "throughput")
        theirs = get(data, scenario, "frp", "throughput")
        out.append(
            f"| {SCENARIO_LABEL.get(scenario, scenario)} "
            f"| {cell(ours, 'mb_per_second', ' MB/s')} "
            f"| {cell(theirs, 'mb_per_second', ' MB/s')} "
            f"| {ratio(ours, theirs, 'mb_per_second')} |"
        )

    out += [
        "\n### Concurrent request latency\n",
        "32 connections doing small round trips. Higher QPS and lower p99 are",
        "better. This is where head-of-line blocking shows up: under loss a",
        "multiplexed tunnel stalls every stream on one lost packet, while",
        "independent connections stall only themselves.\n",
        "| Scenario | OpenFrp QPS | frp QPS | Ratio | OpenFrp p99 | frp p99 |",
        "|---|---:|---:|---:|---:|---:|",
    ]

    for scenario in scenarios:
        ours = get(data, scenario, "openfrp", "latency")
        theirs = get(data, scenario, "frp", "latency")
        out.append(
            f"| {SCENARIO_LABEL.get(scenario, scenario)} "
            f"| {cell(ours, 'qps')} | {cell(theirs, 'qps')} "
            f"| {ratio(ours, theirs, 'qps')} "
            f"| {cell(ours, 'p99_ms', ' ms')} | {cell(theirs, 'p99_ms', ' ms')} |"
        )

    failures = []
    for scenario in scenarios:
        for stack in ("openfrp", "frp"):
            for mode in ("throughput", "latency"):
                for run in get(data, scenario, stack, mode):
                    if run.get("error"):
                        failures.append(f"- {scenario} / {stack} / {mode}: {run['error']}")
                    elif run.get("errors"):
                        failures.append(
                            f"- {scenario} / {stack} / {mode}: {run['errors']} connection errors"
                        )
    if failures:
        out.append("\n### Runs that reported errors\n")
        out.extend(sorted(set(failures)))

    print("\n".join(out))
    return 0


if __name__ == "__main__":
    sys.exit(main())
