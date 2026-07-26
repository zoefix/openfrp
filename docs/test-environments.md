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

### Two environment quirks that cost time

**Only ports 22, 80 and 443 reach the server.** 7000 behaves exactly like 16000,
where nothing listens: accepted in 0 ms and immediately reset, so the traffic
never arrives. Port 80 answers in 4 ms. Deploying a usable client therefore
needs the provider firewall opened for the control port.

**The development machine resolves through a fake-IP proxy.** Every hostname
returns a distinct 198.18.x.x address from the RFC 2544 benchmarking range, and
requests carrying certain Host values are dropped before leaving the machine.
Tests from that machine must pin the address with `--resolve`, and anything
that still fails should be re-checked from the server before being treated as a
routing bug — two hosts looked broken from the laptop and were correct on the
server.

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
