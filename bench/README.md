# Benchmark harness

Runs OpenFrp and frp side by side under identical conditions and emits a
Markdown comparison.

```bash
./bench/run.sh                  # the full matrix, ~10 minutes
./bench/run.sh --quick          # one scenario, for a smoke check
./bench/run.sh --duration 30s   # longer samples, less noise
```

Results land in `bench/results/` as raw JSON plus `summary.md`. Published
numbers live in [`docs/benchmark.md`](../docs/benchmark.md).

## What it measures

**Single-stream throughput.** One connection, bulk transfer, MB/s. This is the
headline number because it is where multiplexing hurts most: a stream cannot
exceed `window / RTT`, so a shared window becomes a hard ceiling as latency
grows, no matter how much bandwidth is available.

**Concurrent request latency.** 32 connections doing small round trips,
reporting QPS and p50/p99/p99.9. This is where head-of-line blocking shows up —
under multiplexing one lost packet stalls every stream sharing the connection.

## Scenarios

| Name | Shaping |
|---|---|
| `lan` | none |
| `rtt-50ms` | 25 ms each way |
| `rtt-100ms` | 50 ms each way |
| `rtt-200ms` | 100 ms each way |
| `loss-1pct` | 25 ms each way, 1% loss |
| `loss-3pct` | 25 ms each way, 3% loss |

## Why the topology is shaped the way it is

```
loadgen ──fast──> server ──shaped WAN──> client ──loopback──> echo
```

**netem is applied to the client, not the server.** That is the WAN link a
router actually sits behind. Shaping the server instead would also delay the
load generator's own traffic and count the latency twice.

**The echo backend runs inside the client container.** The client reaches it
over loopback, and netem is per-device, so `lo` stays unshaped. That keeps the
LAN hop out of the measurement and isolates the tunnel. It also survives the
client restarting — an earlier version ran the backend in a container sharing
the client's network namespace, and a client restart tore that namespace out
from under it.

**netem gets a 200,000-packet queue.** The default is 1,000, which starts
dropping packets once the bandwidth-delay product exceeds it. That would appear
as loss nobody asked for and would quietly invalidate the high-BDP runs.

## Fairness

Both stacks run on the same base image, the same kernel, the same shaping, the
same backend binary, and are measured by the same load generator. Scenarios run
back to back, so a noisy host degrades both rather than favouring one.

**frp is left at its defaults.** `transport.tcpMux` is true and
`transport.poolCount` is 5 because that is what a user actually gets. The one
setting changed is `loginFailExit = false`, which stops frpc exiting when it
races the server's startup — robustness, not tuning.

If you think a setting makes the comparison unfair, change it in
`bench/config/` and re-run. That is the point of shipping the harness rather
than only the numbers.

## Reading the output

`bench/render.py` prints every scenario, **including the ones OpenFrp loses**.
A benchmark that only shows wins is marketing, and the cases where
multiplexing is genuinely competitive are exactly what a reader needs to judge
whether the default suits them.

Runs that recorded errors are listed separately rather than being averaged in.
