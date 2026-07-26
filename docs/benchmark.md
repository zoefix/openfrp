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
| Sample | 10 s per measurement, 2 s warm-up discarded, median of 3 |

frp runs at its defaults — `transport.tcpMux` on, `transport.poolCount` 5 —
because that is what a user gets. OpenFrp runs at its defaults too: no
multiplexing, pool of 16.

## The prediction that was wrong

The plan for this phase set an acceptance criterion of **an order-of-magnitude
single-stream throughput win at 100 ms RTT**. The reasoning was that yamux's
default stream window is 256 KiB, that a stream cannot exceed `window / RTT`,
and that frp would therefore be pinned near 2.5 MB/s on a long path.

**That did not happen. frp sustained 57 MB/s in that scenario — roughly 20×
the predicted ceiling — and the two were within noise of each other.**

The window arithmetic is sound; the premise was not. frp is evidently not
running with a 256 KiB effective window, so it never approaches the ceiling
the prediction was built on. The acceptance criterion was not met, and the
claim has been deleted from the README rather than restated in a weaker form.

The *mechanism* survives — head-of-line blocking across multiplexed streams is
real and large — but it needs packet loss to appear, not merely latency.
Latency alone gives every stream the same round trip to wait through whether
they share a connection or not. Only when a packet is lost does one stream's
retransmission become every stream's problem. The prediction looked for the
effect on the wrong axis.

A second lesson, applied to the harness rather than the code: the first two
runs of this matrix were single samples and disagreed with each other by 4× on
one scenario — more than the effect being measured. Every measurement is now
repeated and reported as a median with its observed range.

## Results

<!-- BENCH_RESULTS_START -->

Median of 3 runs per measurement. Where repetitions disagreed by more than 15%, the observed range is shown beneath the median.

### Single-stream throughput

Higher is better. One connection, so there is no head-of-line blocking
to suffer; this isolates the cost of moving bytes through userspace
versus splicing them in the kernel.

| Scenario | OpenFrp | frp | Ratio |
|---|---:|---:|---:|
| LAN (no shaping) | 1191.13 MB/s<br><sub>1179.23–3808.36</sub> | 498.99 MB/s<br><sub>413.52–746.31</sub> | **2.39×** |
| 50 ms delay | 106.34 MB/s<br><sub>27.07–121.07</sub> | 102.3 MB/s | **1.04×** |
| 100 ms delay | 66.3 MB/s<br><sub>54.83–74.39</sub> | 56.71 MB/s<br><sub>34.35–58.19</sub> | **1.17×** |
| 200 ms delay | 35.91 MB/s | 27.94 MB/s | **1.29×** |
| 50 ms delay, 1% loss | 0.76 MB/s<br><sub>0.6–1.06</sub> | 0.76 MB/s | **1.00×** |
| 50 ms delay, 3% loss | 0.37 MB/s<br><sub>0.34–0.43</sub> | 0.41 MB/s<br><sub>0.34–0.47</sub> | 0.90× |

### Concurrent request latency

32 connections doing small round trips. Higher QPS and lower p99 are
better. This is where head-of-line blocking shows up: under loss a
multiplexed tunnel stalls every stream on one lost packet, while
independent connections stall only themselves.

| Scenario | OpenFrp QPS | frp QPS | Ratio | OpenFrp p99 | frp p99 |
|---|---:|---:|---:|---:|---:|
| LAN (no shaping) | 81264.9 | 55253<br><sub>44139.5–57778.5</sub> | **1.47×** | 0.839 ms | 1.268 ms<br><sub>1.164–2.298</sub> |
| 50 ms delay | 1055.9<br><sub>573.3–1113.1</sub> | 1123.2 | 0.94× | 37.837 ms<br><sub>31.149–125.403</sub> | 30.96 ms |
| 100 ms delay | 614.8 | 518.8<br><sub>334.1–620.7</sub> | **1.19×** | 55.832 ms | 140.259 ms<br><sub>52.923–142.851</sub> |
| 200 ms delay | 310.3 | 310.3<br><sub>233.5–310.4</sub> | **1.00×** | 106.314 ms | 105.985 ms<br><sub>105.892–200.539</sub> |
| 50 ms delay, 1% loss | 1088.1<br><sub>922.4–1097.5</sub> | 555.7<br><sub>344.2–596.6</sub> | **1.96×** | 129.097 ms<br><sub>31.075–254.082</sub> | 724.411 ms<br><sub>507.243–1072.96</sub> |
| 50 ms delay, 3% loss | 956.4 | 273.7<br><sub>175.2–323.1</sub> | **3.49×** | 258.55 ms | 609.691 ms<br><sub>560.685–674.552</sub> |

<!-- BENCH_RESULTS_END -->

## Reading the numbers

Three findings hold across repetitions. Everything else in the table is inside
the noise and should not be read as a result.

### Under loss with concurrency, OpenFrp wins decisively

This is the scenario the whole design decision is about.

With 32 concurrent streams on a lossy path, frp puts all of them behind one TCP
connection. A single lost packet stalls that connection's retransmission queue
and therefore stalls *every* stream at once. OpenFrp gives each stream its own
connection, so a loss stalls only the stream that suffered it.

At 1% loss that is **1.96× the QPS and a p99 of 129 ms against frp's 724 ms**.
At 3% loss it is **3.49× the QPS**, with p99 258 ms against 610 ms.

Not a laboratory curiosity: cross-provider and cross-border paths in China
routinely run at these loss rates, which is the environment this project
targets.

### On a fast, clean link, OpenFrp wins on both axes

Unshaped, throughput is bounded by how fast bytes can be moved rather than by
the network, and that is where `splice(2)` pays — payload passes
kernel-to-kernel and never enters the process, while frp copies every byte
through userspace twice. Small-request QPS is also higher with a better p99.

The throughput figure here is very noisy (see the range under it) because it is
really measuring memory bandwidth on a shared laptop. The direction is
consistent; the magnitude is not.

### Under latency alone, the two are equivalent

At 50, 100 and 200 ms with no loss, both are bounded by round-trip time and the
numbers converge. Multiplexing costs nothing when nothing is being lost, and
independent connections buy nothing.

This is the finding that contradicts the original prediction. Head-of-line
blocking is real, but it needs *loss* to bite — latency alone does not trigger
it, because there is nothing to retransmit.

### Where OpenFrp loses

Single-stream throughput at 3% loss came out slightly behind (0.90×). One
stream has no siblings to be blocked by, so multiplexing costs nothing there,
and what remains is TCP congestion control reacting to loss — which the tunnel
implementation barely influences. The gap is within the run-to-run spread, so
the honest reading is "no difference", not "frp is faster".

## What this means for the defaults

Multiplexing is a trade, not a mistake:

- On a clean link it is free, and on a slow link with a single stream it is
  free as well.
- On a lossy link with concurrency it is expensive, and the cost grows with
  both the loss rate and the number of streams.
- It always forfeits `splice(2)`, which is what caps throughput once the
  network stops being the bottleneck.

OpenFrp therefore defaults to independent connections and keeps
`transport.mux` available for the case it genuinely suits: many tunnels, low
traffic, and a socket budget that matters more than throughput.

## Caveats

- **Single host.** Both stacks run as containers on one machine, so absolute
  throughput on an unshaped link reflects memory bandwidth rather than any
  real network. Treat the LAN row as a copying-cost measurement, not a
  bandwidth figure. Comparisons within a row remain fair; comparisons of
  absolute values against a real deployment do not.
- **Unshaped numbers are very noisy.** Repetitions of the LAN throughput case
  spanned roughly 1,200–3,800 MB/s for OpenFrp and 410–750 for frp. The ranges
  are printed under each median so a reader can see which rows are solid; treat
  any row carrying a wide range as directional only.
- **netem shapes egress only**, applied to the client's uplink. The labels
  describe the configured delay, not a measured round trip.
- **arm64 host.** Both binaries are arm64 builds. Nothing here is
  architecture-specific, but the absolute numbers are not transferable to the
  x86_64 target hardware.
- **One backend shape.** An echo service. A real backend's own latency would
  compress every ratio here.
