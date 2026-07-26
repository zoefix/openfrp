# Test environments

Facts gathered by direct inspection, not assumption. Keep this current — the
deploy pipeline's detection steps are written against exactly these shapes.

## Router (client side)

`root@192.168.9.1` — the OpenWrt box the LuCI app targets.

| | |
|---|---|
| Release | OpenWrt 25.12.2 `r32802-f505120278` |
| Kernel | 6.12.74 |
| Target / arch | `x86/64` · `x86_64` |
| CPU | 12th Gen Intel Core i3-1215U |
| Memory | 16 GB |
| Storage | 233 GB NVMe, overlay 77 MB used |
| Package manager | **apk** — no opkg |
| rpcd modules | `file.so` `ucode.so` `luci.so` `rpcsys.so` |
| LuCI | installed |
| Shell | busybox ash (no `nproc`, no coreutils) |

Consequences:

- Build `.apk` with the **25.12 SDK**. `.ipk` will not install here.
- Every shell script we ship must be POSIX sh.
- Resources are effectively unlimited, which is why cert issuance and DNS
  management live on the router rather than the server.

**Operating constraints the user set:** do not reboot the router, and do not
touch any configuration outside this project.

## Server (public VPS)

`root@64.83.33.99`

| | |
|---|---|
| Distro | Debian GNU/Linux 12 (bookworm) |
| Kernel | 6.1.0-10-amd64 |
| Arch | x86_64 |
| CPU / memory | 2 cores / 1967 MB (1650 MB available) |
| Disk free | 57 GB |
| Init | systemd |
| Firewall | nftables present, **ruleset empty** |
| SSH host key | `ecdsa-sha2-nistp256 SHA256:794oKSSizXBNwcQP/gzTd2sSVCarefIpDigD174HmU4` |
| Deploy tooling present | `curl wget tar gzip sha256sum setcap systemctl useradd` |

### Ports

80 and 443 are **free**, so the vhost listeners can take the standard ports.

Occupied, and to be avoided when allocating tunnel ports:

```
22            sshd
10001, 20809  v2ray
```

The deploy pipeline still has to probe for occupancy rather than assume: the
previous test host had 80 and 443 taken by an unrelated service, and the
correct behaviour there is to report the conflict, not to fight for the port.

### BBR is available but not loaded

```
net.ipv4.tcp_congestion_control          = cubic
net.ipv4.tcp_available_congestion_control = reno cubic
modinfo tcp_bbr → /lib/modules/6.1.0-10-amd64/kernel/net/ipv4/tcp_bbr.ko
```

The module ships with the kernel but is not loaded, which is why BBR does not
appear in the available set. The deploy step must therefore **`modprobe
tcp_bbr` first, then set the sysctl, then persist both** — setting the sysctl
alone fails silently while the module is absent. Verified on the previous host:
after loading, the available set became `reno cubic bbr`.

## Verified against real domains

`*.aiqno.com` and `test.2rd.aiqno.com` both resolve to the server. Routing was
checked end to end through the deployed server on port 80:

| Host | Reached | Correct |
|---|---|---|
| `test.aiqno.com` | wildcard tunnel | ✓ |
| `foo.aiqno.com` | wildcard tunnel | ✓ |
| `2rd.aiqno.com` | wildcard tunnel | ✓ |
| `test.2rd.aiqno.com` | **exact tunnel** | ✓ |
| `a.b.aiqno.com` | 404 | ✓ |
| `deep.test.2rd.aiqno.com` | 404 | ✓ |
| `aiqno.com` | 404 | ✓ |

The decisive row is `test.2rd.aiqno.com`: it reached the tunnel that claimed it
by name rather than the `*.aiqno.com` tunnel, which is both exact-beats-wildcard
and proof that a `*` label does not span two levels.

Note that DNS is set up the same way for the same reason — a `*.aiqno.com`
record does not cover `test.2rd.aiqno.com` either, which is why that name needs
its own record. The routing rule deliberately mirrors the DNS rule.

### OpenClash silently eats the control connection

This one cost the most time and is worth reading before debugging any
"connection lost / login: EOF" report.

The control port looked firewalled. It was not. **OpenClash on the router
transparently proxies all TCP**, via an unconditional nft rule:

```
ip protocol tcp counter redirect to :7892
```

That captures the router's own outbound traffic as well as the LAN's. Clash
then applies its rule list, and its log shows exactly what happened:

```
[TCP] ...:53239 --> 64.83.33.99:22   match DstPort(22) using SSH[…直连]
[TCP] ...:39868 --> 64.83.33.99:7000 match Match      using 其他[香港 A05 …]
```

Port 22 has a specific rule and goes direct. Port 7000 falls through to the
catch-all `MATCH` and is routed through a Hong Kong proxy node that cannot
relay it, so the connection is accepted locally and then dropped. The server
never sees a packet — its log stays empty while the client reports EOF after a
successful TCP connect.

A further wrinkle in this particular setup: the VPS is itself one of the
configured Clash nodes (`server: 64.83.33.99` in the profile), so Clash was
trying to tunnel traffic bound for that host through another proxy.

The fix is one rule, placed above the final `MATCH`:

```yaml
- "IP-CIDR,<server-ip>/32,DIRECT,no-resolve"
```

**This is not an exotic setup.** OpenClash, Passwall, ShellCrash and similar are
extremely common on exactly the routers this project targets, and all of them
install a catch-all TCP redirect. Expect this to be the single most frequent
support question, and note the symptom precisely: a TCP connect that succeeds
in 0 ms — impossible for a remote host — followed by an immediate close.

### The development machine resolves through a fake-IP proxy

Every hostname returns a distinct 198.18.x.x address from the RFC 2544
benchmarking range, and requests carrying certain Host values are dropped
before leaving the machine. Tests from that machine must pin the address with
`--resolve`, and anything that still fails should be re-checked from the server
before being treated as a routing bug — two hosts looked broken from the laptop
and were correct on the server.

## SSH automation

`sshpass` on macOS fails against this host: its pty interception does not catch
the prompt, ssh falls through to an askpass helper, and the password is never
sent — which burns real authentication attempts and looks exactly like a wrong
password.

`golang.org/x/crypto/ssh` authenticates successfully with the same credential.

This is direct justification for the plan's decision to implement SSH inside
the Go binary rather than shelling out to a client: we control the auth path,
get structured errors instead of scraped stderr, and avoid depending on which
options the local ssh client was built with.
