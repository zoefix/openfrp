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
