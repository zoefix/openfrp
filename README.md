# OpenFrp

High-performance NAT traversal for OpenWrt, with DNS management and TLS
certificate automation built in.

Three pieces:

| | |
|---|---|
| **`openfrps`** | Public server. Moves tunnel traffic and nothing else — no cloud credentials live here. |
| **`openfrpc`** | Runs on the router. Maintains tunnels, provisions the server over SSH, and (from P5) manages DNS and certificates locally. |
| **`luci-app-openfrp`** | The LuCI web UI. Every management action happens here. |

Status: **P0 complete** — protocol, transport, server, client and TCP tunnels
are working end to end. See [the roadmap](#roadmap).

## Why it is faster than frp

frp has three structural bottlenecks. Each one is addressed by a specific
decision here, not by general optimisation.

**1. frp multiplexes by default.** Every work stream shares one TCP connection
via yamux, so all tunnels sit behind a single congestion window and a single
retransmission queue — one lost packet stalls everything at once. Worse, the
yamux default stream window is 256 KiB, and a stream cannot exceed
`window / RTT`. Over a 100 ms path that caps a single stream at roughly
**2.5 MB/s regardless of available bandwidth**.

OpenFrp defaults to a **connection pool**: each work connection is its own TCP
connection with its own congestion window. Multiplexing is available
(`transport.mux`) but opt-in, and when enabled the window defaults to 8 MiB
rather than 256 KiB.

**2. frp copies every byte through userspace.** NIC → kernel → frps → kernel →
frpc → kernel → NIC.

Because our work connections are bare TCP sockets, `netutil.Relay` hands the
transfer to **`splice(2)`** and payload never enters the process at all. This
is verified, not assumed: `TestEndToEndUsesKernelFastPath` fails the build if
any hop of a plain TCP tunnel falls back to a userspace copy on Linux, and
`RelayCounts()` exposes the split at runtime.

The corollary is a rule this codebase enforces: **never wrap a work connection
in a type that hides the underlying `*net.TCPConn`**. A wrapper that does not
transform bytes may implement `netutil.Unwrapper` to stay on the fast path;
anything that transforms bytes (TLS, compression) must not. TLS is therefore
applied to the control connection only — see the comment on `Dialer.DialWork`.

**3. frps cannot be reconfigured at runtime.** Its API is read-only, so
changing a port, a token, or a certificate means restarting the process and
dropping every connected client. Certificate rotation in particular
([frp#2946](https://github.com/fatedier/frp/issues/2946)) is a full
disconnect.

OpenFrp's routing table is swapped atomically, and certificates are pushed up
the control connection and hot-loaded — zero dropped connections.

Additional wins: `SO_REUSEPORT` multi-accept removes accept-queue lock
contention, and the SSH provisioner enables BBR on the server as part of
deployment.

None of this is a claim until it is measured. The `bench/` harness (P1) runs
frp and OpenFrp side by side under identical `tc netem` conditions, and
`docs/benchmark.md` will publish the numbers **including the cases we lose**.

## Build

```bash
make build
```

Both binaries are static and dependency-free (`CGO_ENABLED=0`), so one build
runs on Alpine, Debian oldstable, CentOS and OpenWrt alike.

```bash
make check       # vet, gofmt, race-enabled tests
make test-linux  # run the suite on Linux, where splice(2) actually engages
make cross       # release artefacts for every target platform
```

`make test-linux` matters: the fast-path assertions are skipped on macOS
because `splice(2)` is Linux-only. A green run on your laptop does not prove
the performance property holds.

## Try it locally

```bash
make dev-up
```

Then, in another shell:

```bash
curl -s http://localhost:6080/
```

That request reaches an nginx container through a real tunnel. The three
containers mirror production topology: `service` is the LAN service, `openfrpc`
is the router, `openfrps` is the public host.

```bash
make dev-down
```

## Configuration

JSON. See [`configs/`](configs/) for annotated examples. On OpenWrt the init
script renders UCI into `/var/etc/openfrp.json`, so the daemon only ever has to
understand one format.

Unknown fields are rejected rather than ignored — a typo in a key should fail
loudly, not silently disable the thing you meant to configure.

### Domain patterns

A `*` label matches **exactly one** level, and may appear at any depth:

| Pattern | Matches | Does not match |
|---|---|---|
| `aaa.com` | `aaa.com` | any subdomain |
| `*.aaa.com` | `www.aaa.com` | `x.bb.aaa.com` |
| `*.bb.aaa.com` | `x.bb.aaa.com` | `y.x.bb.aaa.com` |

Exact names beat wildcards; deeper wildcards beat shallower ones.

This differs from frp, where `*.aaa.com` also matches `x.bb.aaa.com`. Ours
matches DNS and TLS certificate semantics — a Let's Encrypt `*.aaa.com` covers
one level only — so a route can never succeed while its certificate fails.

## Layout

Packages are organised by domain, not by technical layer.

```
cmd/                     entrypoints, one subcommand per file
internal/
  tunnel/
    protocol/            wire format, shared by both daemons
    transport/           TCP dialer, opt-in yamux
    vhost/               domain routing            (P1)
    server/  client/     the two daemons
  dns/                   DNS providers             (P5)
  cert/                  ACME issuance             (P6)
  deploy/                SSH provisioning          (P3)
  storage/               SQLite via modernc.org/sqlite (pure Go, no CGO)
pkg/
  netutil/               splice, buffers, socket options — the hot path
  cloudapi/              cloud API signing         (P4)
openwrt/                 OpenWrt feed and LuCI app (P2)
```

Rules that hold throughout: one DNS provider per package; registries populated
via `init()` so adding a provider touches no existing file; interfaces declared
by the consumer; `pkg/` free of business logic.

## Roadmap

| | | |
|---|---|---|
| **P0** | protocol, transport, server, client, TCP tunnels | ✅ done |
| P1 | domain routing, HTTP proxy, SNI passthrough, `bench/` | |
| P2 | OpenWrt `.apk` and LuCI app | |
| P3 | SSH server provisioning | |
| P4 | cloud API signing, schema-driven forms | |
| P5 | DNS management, first five providers | |
| P6 | ACME issuance, edge TLS termination, hot cert reload | |
| P7–P11 | remaining providers, renewal, status panel, QUIC/KCP, eBPF sockmap | |

## Target environments

Both test systems are documented in
[`docs/test-environments.md`](docs/test-environments.md) with the constraints
they impose — including that the router runs OpenWrt 25.12 (so packages must be
`.apk`, not `.ipk`) and that ports 80 and 443 on the test server are already
occupied.

## Licence

MIT. See [LICENSE](LICENSE) and [NOTICE](NOTICE) for third-party attribution.
