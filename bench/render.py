#!/usr/bin/env python3
"""Render benchmark JSON into a Markdown comparison.

Deliberately reports every scenario, including the ones OpenFrp loses. A
benchmark that only shows wins is marketing, and the cases where multiplexing
is genuinely competitive are the ones a reader most needs to see.
"""

import json
import pathlib
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
    "rtt-50ms": "50 ms RTT",
    "rtt-100ms": "100 ms RTT",
    "rtt-200ms": "200 ms RTT",
    "loss-1pct": "50 ms RTT, 1% loss",
    "loss-3pct": "50 ms RTT, 3% loss",
}


def load(results_dir: pathlib.Path) -> dict:
    data = {}
    for path in results_dir.glob("*.json"):
        stem = path.stem
        try:
            scenario, stack, mode = stem.rsplit("-", 2)
        except ValueError:
            continue
        try:
            payload = json.loads(path.read_text())
        except (json.JSONDecodeError, OSError):
            continue
        data.setdefault(scenario, {}).setdefault(stack, {})[mode] = payload
    return data


def ratio(ours: float, theirs: float) -> str:
    """Format the speed-up, or a plain marker when it is not meaningful."""
    if not theirs:
        return "—"
    factor = ours / theirs
    if factor >= 1:
        return f"**{factor:.2f}×**"
    return f"{factor:.2f}×"


def cell(value, unit: str = "") -> str:
    if value is None:
        return "—"
    return f"{value}{unit}"


def main() -> int:
    results_dir = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "bench/results")
    data = load(results_dir)
    if not data:
        print("No results found. Run ./bench/run.sh first.", file=sys.stderr)
        return 1

    scenarios = [s for s in SCENARIO_ORDER if s in data]
    scenarios += sorted(s for s in data if s not in SCENARIO_ORDER)

    out = []
    out.append("### Single-stream throughput\n")
    out.append("Higher is better. This is where multiplexing hurts most: a stream")
    out.append("cannot exceed window/RTT, so a shared 256 KiB window becomes the")
    out.append("ceiling as latency grows.\n")
    out.append("| Scenario | OpenFrp | frp | Ratio |")
    out.append("|---|---:|---:|---:|")

    for scenario in scenarios:
        ours = data[scenario].get("openfrp", {}).get("throughput", {})
        theirs = data[scenario].get("frp", {}).get("throughput", {})
        o = ours.get("mb_per_second")
        f = theirs.get("mb_per_second")
        out.append(
            f"| {SCENARIO_LABEL.get(scenario, scenario)} "
            f"| {cell(o, ' MB/s')} | {cell(f, ' MB/s')} "
            f"| {ratio(o or 0, f or 0)} |"
        )

    out.append("\n### Concurrent request latency\n")
    out.append("32 connections doing small round trips. Lower is better.\n")
    out.append("| Scenario | OpenFrp QPS | frp QPS | OpenFrp p99 | frp p99 |")
    out.append("|---|---:|---:|---:|---:|")

    for scenario in scenarios:
        ours = data[scenario].get("openfrp", {}).get("latency", {})
        theirs = data[scenario].get("frp", {}).get("latency", {})
        out.append(
            f"| {SCENARIO_LABEL.get(scenario, scenario)} "
            f"| {cell(ours.get('qps'))} | {cell(theirs.get('qps'))} "
            f"| {cell(ours.get('p99_ms'), ' ms')} | {cell(theirs.get('p99_ms'), ' ms')} |"
        )

    errors = []
    for scenario in scenarios:
        for stack in ("openfrp", "frp"):
            for mode, payload in data[scenario].get(stack, {}).items():
                if payload.get("error") or payload.get("errors"):
                    errors.append(
                        f"- {scenario} / {stack} / {mode}: "
                        f"{payload.get('error') or str(payload.get('errors')) + ' errors'}"
                    )
    if errors:
        out.append("\n### Runs that reported errors\n")
        out.extend(errors)

    print("\n".join(out))
    return 0


if __name__ == "__main__":
    sys.exit(main())
