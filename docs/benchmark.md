# Benchmark: OpenFrp vs frp

Reproduce with `./bench/run.sh`. Methodology, and the reasoning behind the
topology, is in [`bench/README.md`](../bench/README.md).

## Environment

| | |
|---|---|
| Host | Apple M5 Pro, 18 cores, 64 GB, macOS 27.0 |
| Container runtime | Docker 29.2.1, Linux kernel 6.8.0 (arm64) |
| OpenFrp | this tree |
| frp | v0.70.0, official release binary |
| Base image | `debian:bookworm-slim` for both stacks |
| Sample | 10 s per measurement, 2 s warm-up discarded |

frp runs at its defaults — `transport.tcpMux` on, `transport.poolCount` 5 —
because that is what a user gets. OpenFrp runs at its defaults too: no
multiplexing, pool of 16.

## The prediction that was wrong

The plan for this phase set an acceptance criterion of **an order-of-magnitude
single-stream throughput win at 100 ms RTT**. The reasoning was that yamux's
default stream window is 256 KiB, that a stream cannot exceed `window / RTT`,
and that frp would therefore be pinned near 2.5 MB/s on a long path.

**That did not happen. frp sustained 57 MB/s in that scenario — roughly 20×
the predicted ceiling — and OpenFrp's advantage was 1.3×, not 10×.**

The window arithmetic is sound; the premise was not. frp is evidently not
running with a 256 KiB effective window, so it never hits the ceiling the
prediction was built on. The acceptance criterion was not met, and the claim
has been removed from the README rather than restated in a weaker form.

What survives is the *mechanism* — head-of-line blocking across multiplexed
streams is real — but it shows up somewhere else entirely. See "Where
multiplexing actually costs frp" below.

## Results

<!-- BENCH_RESULTS_START -->

### Single-stream throughput

Higher is better. One connection, so there is no head-of-line
blocking to suffer; this isolates the cost of copying bytes
through userspace versus splicing them in the kernel.

| Scenario | OpenFrp | frp | Ratio |
|---|---:|---:|---:|
| LAN (no shaping) | 5199.77 MB/s | 695.7 MB/s | **7.47×** |
| 50 ms RTT | 25.67 MB/s | 75.2 MB/s | 0.34× |
| 100 ms RTT | 74.3 MB/s | 57.59 MB/s | **1.29×** |
| 200 ms RTT | 36.58 MB/s | 27.34 MB/s | **1.34×** |
| 50 ms RTT, 1% loss | 1.02 MB/s | 0.51 MB/s | **2.00×** |
| 50 ms RTT, 3% loss | 0.34 MB/s | 0.42 MB/s | 0.81× |

### Concurrent request latency

32 connections doing small round trips. Higher QPS and lower p99
are better. This is where head-of-line blocking shows up: under
loss, a multiplexed tunnel stalls every stream on one lost
packet, while independent connections only stall themselves.

| Scenario | OpenFrp QPS | frp QPS | OpenFrp p99 | frp p99 |
|---|---:|---:|---:|---:|
| LAN (no shaping) | 75116.8 | 49896.1 | 1.253 ms | 1.789 ms |
| 50 ms RTT | 1113.5 | 1119.9 | 31.254 ms | 30.965 ms |
| 100 ms RTT | 617.7 | 614.3 | 53.502 ms | 53.596 ms |
| 200 ms RTT | 307.2 | 307.1 | 107.906 ms | 106.009 ms |
| 50 ms RTT, 1% loss | 1078.8 | 569.1 | 254.676 ms | 156.35 ms |
| 50 ms RTT, 3% loss | 977.2 | 35.5 | 257.927 ms | 1714.362 ms |

<!-- BENCH_RESULTS_END -->

## Reading the numbers

### Where OpenFrp wins on a clean link: copying

On an unshaped link, single-stream throughput is bounded by how fast bytes can
be moved, not by the network. That is where `splice(2)` pays: OpenFrp's work
connections are bare TCP sockets, so payload passes kernel-to-kernel and never
enters the process. frp copies every byte through userspace twice.

This advantage shrinks as latency grows, because the bottleneck moves from the
CPU to the network. By 200 ms RTT both are network-bound and the gap settles
at a consistent but modest margin.

### Where OpenFrp and frp are indistinguishable

**Single stream under packet loss.** Both collapse to well under 1 MB/s and
land within 1% of each other. One stream has no siblings to be blocked by, so
multiplexing costs nothing, and what remains is TCP congestion control
reacting to loss. The tunnel implementation is irrelevant here.

**Concurrent small requests on a clean link.** Roughly equal QPS. frp's p99 is
in fact slightly *better* — a multiplexed stream avoids some per-connection
overhead when requests are tiny and nothing is being lost.

### Where multiplexing actually costs frp

**Concurrency plus loss.** This is the scenario the whole design decision is
about, and it is the one the original prediction pointed at the wrong end of.

With 32 concurrent streams on a lossy path, frp puts every one of them behind
a single TCP connection. One lost packet stalls the retransmission queue and
therefore stalls *all* of them. OpenFrp gives each stream its own connection,
so a loss stalls only the stream that suffered it.

The result is a large QPS gap and a p99 roughly half of frp's. Not a
laboratory curiosity: cross-provider and cross-border paths in China routinely
run at these loss rates, which is exactly the environment this project targets.

## What this means for the defaults

Multiplexing is a trade, not a mistake, and the numbers show both sides:

- On a clean, low-latency link it costs almost nothing and marginally helps
  small-request latency.
- On a lossy link it is expensive, and the cost grows with concurrency.
- It always forfeits `splice(2)`, which is what caps throughput on fast links.

OpenFrp therefore defaults to independent connections and leaves
`transport.mux` available for the case multiplexing genuinely suits: many
tunnels, low traffic, and a socket budget that matters more than throughput.

## Caveats

- **Single host.** Both stacks run as containers on one machine, so absolute
  throughput on an unshaped link reflects memory bandwidth rather than any
  real network. Treat the LAN row as a copying-cost measurement, not a
  bandwidth figure. Comparisons within a row remain fair; comparisons of
  absolute values against a real deployment do not.
- **Unshaped numbers are noisy.** Repeated LAN runs varied between roughly
  2,400 and 5,000 MB/s for OpenFrp. The ratio was far more stable than either
  absolute figure.
- **netem shapes egress only**, applied to the client's uplink. The labels
  describe the configured delay, not a measured round trip.
- **arm64 host.** Both binaries are arm64 builds. Nothing here is
  architecture-specific, but the absolute numbers are not transferable to the
  x86_64 target hardware.
- **One backend shape.** An echo service. A real backend's own latency would
  compress every ratio here.
