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

`root@154.36.180.102`

| | |
|---|---|
| Distro | Debian GNU/Linux 12 (bookworm) |
| Kernel | 6.1.0-10-amd64 |
| Arch | x86_64 |
| CPU / memory | 2 cores / 1967 MB |
| Disk free | 58 GB |
| Init | systemd |
| Firewall | nftables present, **ruleset empty** |
| SSH host key | `ecdsa-sha2-nistp256 SHA256:dTeZDwBSiviam4DzDqWn8AGWZtVhA0ohAiZMOx1loqI` |

### Port 80 and 443 are already taken

```
LISTEN *:443  users:(("nekoshare",pid=596698,fd=3))
LISTEN *:80   users:(("nekoshare",pid=596698,fd=6))
```

An unrelated service owns both. The vhost listeners cannot claim them without
displacing it, so `vhost_http_port` / `vhost_https_port` must be configurable
and the deploy pipeline must **detect the conflict and refuse rather than
fight for the port**. This is the concrete case the `detect` step exists for.

### BBR is available but not active

Out of the box:

```
net.ipv4.tcp_congestion_control = cubic
net.ipv4.tcp_available_congestion_control = reno cubic
```

After `modprobe tcp_bbr` the available set becomes `reno cubic bbr`. So the
deploy step that enables BBR must load the module first, then set the sysctl,
then persist both — the sysctl alone silently fails while the module is absent.

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
