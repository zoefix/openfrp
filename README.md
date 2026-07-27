# OpenFrp

High-performance NAT traversal for OpenWrt, with DNS management and TLS
certificate automation built in.

Three pieces:

| | |
|---|---|
| **`openfrps`** | Public server. Moves tunnel traffic and nothing else — no cloud credentials live here. |
| **`openfrpc`** | Runs on the router. Maintains tunnels, provisions the server over SSH, and manages DNS and certificates locally. |
| **`luci-app-openfrp`** | The LuCI web UI: status, tunnels, server provisioning, DNS and certificates. Every management action happens here. |

Status: tunnels (TCP, UDP and domain-routed HTTP/HTTPS), the OpenWrt package
and LuCI app, one-command server provisioning, DNS management, ACME issuance
with zero-downtime rotation, renewal scheduling and traffic accounting all
work and are tested on real hardware. See
[Known gaps](#known-gaps) for what does not.

## Provisioning a server

```bash
openfrpc deploy -host 203.0.113.10 -binary ./dist/openfrps_linux_amd64
```

Detects the distribution, architecture and init system; creates a service
user; uploads and checksums the binary; writes the configuration; installs a
systemd, OpenRC or sysvinit service; opens the firewall; enables BBR; and
verifies the result. Re-running upgrades in place — every step is idempotent.

From LuCI it is the **Deploy now** button, which asks for the SSH password at
that moment and never stores it. Key authentication is offered alongside.

A password is never accepted as a flag, and never reaches a command line at
all, because `/proc/*/cmdline` is readable by every local process on the
router — a password in an argument is a password published to anyone with a
shell. It travels from the browser to the job worker through a mode-0600 file
on tmpfs that the worker unlinks the moment it has read it. From a shell, pass
credentials as JSON on stdin with `-stdin`. `-dry-run` prints the plan without
touching anything.

The router bundles a server binary built for its own architecture and uploads
it, so the server needs no outbound internet and gets bytes the router
checksummed. When the server turns out to be a different architecture, the
deployer notices from the ELF header and downloads the right one instead of
installing something that cannot execute.

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

A tunnel that terminates TLS names the certificate it uses, and only a bound
tunnel has one pushed. With several certificates on file, choosing one
automatically would eventually serve the wrong name — which a browser reports
to the visitor as an impersonation attempt, not as a misconfiguration.

Termination offers **HTTP/1.1 only**. The decrypted stream is relayed to the
LAN service unchanged, so whatever is negotiated here is spoken directly at
that service; advertising HTTP/2 promises a protocol the other end was never
asked about. Measured against a real nginx backend, doing so returned 421
where HTTP/1.1 returned 200.

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

## Languages

The LuCI app ships English, 简体中文, 繁體中文 (台灣) and 日本語. English is the
source language and needs no catalogue; the other three live in
[`openwrt/luci/luci-app-openfrp/po/`](openwrt/luci/luci-app-openfrp/po/) and
build into `luci-i18n-openfrp-{zh-cn,zh-tw,ja}`.

`tools/luci-i18n` replaces the parts of LuCI's toolchain that only exist inside
a buildroot, so catalogues can be extracted, compiled and checked from a normal
checkout:

```bash
go run ./tools/luci-i18n extract openwrt/luci/luci-app-openfrp/po/templates/openfrp.pot openwrt/luci/luci-app-openfrp/htdocs
```

`go test ./tools/...` fails when a string reachable from the UI has no
translation, when a translation loses or reorders a `%s`, and when the hash
drifts from LuCI's. That last one matters more than it looks: the hash is how
LuCI keys every entry, and one wrong bit yields a catalogue that loads without
error and translates nothing. It is pinned against keys read out of a stock
`base.zh-cn.lmo` taken off a running router.

Selecting 繁體中文 or 日本語 translates this app, but the rest of the LuCI
interface stays English unless the matching `luci-i18n-base-*` package is
installed — that is a separate package, not something this one can supply.

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
tools/                   build-time helpers, one command per directory
```

Rules that hold throughout: one DNS provider per package; registries populated
via `init()` so adding a provider touches no existing file; interfaces declared
by the consumer; `pkg/` free of business logic.

## Roadmap

| | | |
|---|---|---|
| **P0** | protocol, transport, server, client, TCP tunnels | ✅ done |
| **P1** | wildcard domain routing, HTTP vhost, SNI passthrough, `bench/` | ✅ done |
| **P2** | OpenWrt `.apk` and LuCI app | ✅ done |
| **P3** | SSH server provisioning | ✅ done |
| **P4** | cloud API signing, schema-driven forms | ✅ done |
| **P5** | DNS management, seven providers | ✅ done |
| **P6** | ACME issuance, edge TLS termination, hot cert reload | ✅ done |
| P7 | the remaining twelve DNS providers | in progress |
| P8–P9 | renewal scheduling, traffic accounting | ✅ done |
| P10 | QUIC and KCP transports, bandwidth limits | accepted in config, behave as TCP |
| P11 | eBPF sockmap | not started |

### Known gaps

- **QUIC and KCP are accepted in configuration and behave as TCP.** The
  transport selector exists; the transports do not.
- **Twelve of the nineteen planned DNS providers are unwritten.** The seven
  present are Aliyun, DNSPod, Huawei, Cloudflare, NameSilo, PowerDNS and West.
- **DNS and certificate management need SQLite, which has no MIPS port.** On a
  MIPS router those two pages report themselves unavailable; tunnels are
  unaffected.
- Renewal reaches a running client by polling the database once a minute
  rather than being told, because issuance runs in a separate process. A
  renewed certificate is therefore live within a minute, not instantly.

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
