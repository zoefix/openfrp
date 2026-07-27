# OpenFrp

High-performance NAT traversal for OpenWrt, with DNS management and TLS
certificate automation built in.

Three pieces:

| | |
|---|---|
| **`openfrps`** | Public server. Moves tunnel traffic and nothing else — no cloud credentials live here. |
| **`openfrpc`** | Runs on the router. Maintains tunnels, provisions the server over SSH, and manages DNS and certificates locally. |
| **`luci-app-openfrp`** | The LuCI web UI. Every management action happens here. |

Status: **P0–P6 and P8–P10 complete**; P7 partially. Tunnels (TCP, UDP and
domain-routed HTTP/HTTPS), the OpenWrt package and LuCI app, one-command
server provisioning, DNS management, ACME issuance with zero-downtime
rotation, renewal scheduling and traffic accounting are all working and
tested. See [the roadmap](#roadmap) for what is not.

## Provisioning a server

```bash
openfrpc deploy -host 203.0.113.10 -binary ./dist/openfrps_linux_amd64
```

Detects the distribution, architecture and init system; creates a service
user; uploads and checksums the binary; writes the configuration; installs a
systemd, OpenRC or sysvinit service; opens the firewall; enables BBR; and
verifies the result. Re-running upgrades in place — every step is idempotent.

A password is never accepted as a flag, because `/proc/*/cmdline` is readable
by every local process on the router. Pass credentials as JSON on stdin with
`-stdin`, or use key authentication. `-dry-run` prints the plan without
touching anything.

## Domain routing

Any number of tunnels share ports 80 and 443, routed by name. A `*` label
matches **exactly one** level and may appear at any depth:

| Pattern | Matches | Does not match |
|---|---|---|
| `aaa.com` | `aaa.com` | any subdomain |
| `*.aaa.com` | `www.aaa.com` | `x.bb.aaa.com` |
| `*.bb.aaa.com` | `x.bb.aaa.com` | `y.x.bb.aaa.com` |

Exact names beat wildcards, deeper wildcards beat shallower ones, and a bare
`*` is an opt-in catch-all.

This differs from frp, where `*.aaa.com` also matches `x.bb.aaa.com`. Ours
mirrors DNS and TLS certificate scope — a Let's Encrypt `*.aaa.com` covers one
level — so a route can never resolve to a tunnel whose certificate does not
cover the name. Under frp's rule that mismatch is silent and unpleasant to
debug.

HTTPS is routed on the TLS SNI **without decrypting** by default: the server
forwards ciphertext untouched and the backend owns the certificate. Edge
termination — where the router issues the certificate and pushes it up — is
available per tunnel via `tls_mode`, and costs `splice(2)` for that connection
since a decrypting proxy cannot hand the kernel a raw socket.

## Why it is faster than frp

frp has three structural bottlenecks. Each one is addressed by a specific
decision here, not by general optimisation.

**1. frp multiplexes by default.** Every work stream shares one TCP connection
via yamux, so all tunnels sit behind a single congestion window and a single
retransmission queue — one lost packet stalls every tunnel at once.

OpenFrp defaults to a **connection pool**: each work connection is its own TCP
connection with its own congestion window. Multiplexing is available
(`transport.mux`) but opt-in, and when enabled the window defaults to 8 MiB
rather than yamux's 256 KiB.

Measured against frp on a lossy link with 32 concurrent streams, this is worth
**3.5× the QPS at 3% loss** and roughly a fifth of the p99 at 1% loss. On a
clean link it is worth nothing at all: latency alone does not trigger
head-of-line blocking, because there is nothing to retransmit. See
[the benchmark](docs/benchmark.md), which also records the prediction this
project got wrong.

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

None of this is a claim until it is measured. The [`bench/`](bench/) harness
runs frp and OpenFrp side by side under identical `tc netem` conditions, and
[`docs/benchmark.md`](docs/benchmark.md) publishes the numbers **including the
scenarios where OpenFrp ties or loses, and one where the original theory was
simply wrong**.

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

Domain patterns for `http` and `https` tunnels follow the rules in
[Domain routing](#domain-routing) above.

## Layout

Packages are organised by domain, not by technical layer.

```
cmd/                     entrypoints, one subcommand per file
bench/                   side-by-side comparison against frp
internal/
  tunnel/
    protocol/            wire format, shared by both daemons
    transport/           TCP dialer, opt-in yamux
    vhost/               wildcard routing, Host and SNI sniffing
    server/  client/     the two daemons
  dns/                   DNS management and providers
  cert/                  ACME issuance and renewal
  deploy/                SSH provisioning
  scheduler/             periodic jobs
  stats/                 traffic accounting
pkg/
  netutil/               splice, buffers, socket options — the hot path
  cloudapi/              cloud API request signing
  schema/                declarative provider forms
openwrt/                 OpenWrt feed and LuCI app
```

Rules that hold throughout: one DNS provider per package; registries populated
via `init()` so adding a provider touches no existing file; interfaces declared
by the consumer; `pkg/` free of business logic.

## Roadmap

| | | |
|---|---|---|
| **P0** | protocol, transport, server, client, TCP tunnels | ✅ done |
| **P1** | wildcard domain routing, HTTP vhost, SNI passthrough, `bench/` | ✅ done |
| P2 | OpenWrt `.apk` and LuCI app | |
| **P3** | SSH server provisioning | ✅ done |
| P4 | cloud API signing, schema-driven forms | |
| P5 | DNS management, first five providers | |
| P6 | ACME issuance, edge TLS termination, hot cert reload | |
| P7–P11 | remaining providers, renewal, status panel, QUIC/KCP, eBPF sockmap | |

## If the client cannot connect

A transparent proxy on the router is the most likely cause, not a firewall.
OpenClash, Passwall and ShellCrash all install an unconditional TCP redirect,
and the control port usually falls through to their catch-all rule and gets
routed via a proxy node that cannot relay it.

The symptom is distinctive: **the TCP connect succeeds in 0 ms** — impossible
for a remote host — and the connection is then closed, so the client reports
`login: EOF` while the server's log stays completely empty.

Fix it with a direct rule ahead of the final `MATCH`:

```yaml
- "IP-CIDR,<server-ip>/32,DIRECT,no-resolve"
```

## Target environments

Both test systems are documented in
[`docs/test-environments.md`](docs/test-environments.md) with the constraints
they impose — including that the router runs OpenWrt 25.12, so packages must be
`.apk` rather than `.ipk`.

## Licence

MIT. See [LICENSE](LICENSE) and [NOTICE](NOTICE) for third-party attribution.
