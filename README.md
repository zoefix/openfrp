<div align="center">

# OpenFrp

---

Reach the services on your home network from anywhere. An OpenWrt package with
a web interface for tunnels, domains and HTTPS certificates.

[English](README.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [日本語](README.ja.md)

![Version](https://img.shields.io/badge/VERSION-v0.3.0-8A2BE2?style=for-the-badge&labelColor=444)
![OpenWrt](https://img.shields.io/badge/OPENWRT-SUPPORTED-00B5E2?style=for-the-badge&labelColor=444)
![apk](https://img.shields.io/badge/APK-SUPPORTED-000000?style=for-the-badge&labelColor=444)
![opkg](https://img.shields.io/badge/OPKG-SUPPORTED-F5A623?style=for-the-badge&labelColor=444)

</div>

## What this is for

Your NAS, your home server, a camera, a website you run at home — none of it is
reachable from outside, because your router has no public address.

OpenFrp fixes that with a cheap VPS as the front door. Traffic arrives at the
VPS and is carried to the machine on your LAN through a tunnel your router
keeps open.

```
visitor ──▶ your VPS (public IP) ──tunnel──▶ your router ──▶ NAS / server / camera
```

You need two things:

- **A router running OpenWrt**, where the package and the web interface are
  installed.
- **A VPS with a public IP.** Any provider, any size — the cheapest one works.
  You need SSH access to it once, for setup.

A domain is optional, but without one you reach services by `IP:port`. With
one you get `nas.example.com` and real HTTPS.

## Install

### If your OpenWrt feed has the package

```bash
apk update && apk add luci-app-openfrp
```

On OpenWrt 24.10 and older, which use opkg:

```bash
opkg update && opkg install luci-app-openfrp
```

### Otherwise, use the installer

```bash
wget -O - https://raw.githubusercontent.com/zoefix/openfrp/main/scripts/install.sh | sh
```

It works out your router's architecture, picks the matching release, checks it
against the published checksums, and installs it with `apk` or `opkg` —
whichever your system uses. Run the same command again to upgrade.

For a Chinese-language interface:

```bash
apk add luci-i18n-openfrp-zh-cn      # or opkg install …
```

Then open **LuCI → Services → OpenFrp**. Log out and back in if the menu is
not there yet.

## Setting up

### Step 1 — set up the VPS

Go to the **Servers** tab, **Add server**, fill in:

| | |
|---|---|
| Address | your VPS's IP |
| SSH user | usually `root` |
| SSH password | asked for now, never stored unless you tick the box |

Press **Deploy over SSH**. This installs the server side on the VPS: it works
out the distribution and init system, uploads the binary, writes a service,
opens the firewall and starts it. Takes about half a minute; the log appears
as it runs.

When it finishes the connection token is filled in for you. The **Status** tab
should show the server as connected.

> Already running OpenFrp on that VPS? Skip the deploy — just fill in the
> address, port and token by hand.

### Step 2 — add a tunnel

Go to **Tunnels**, **Add**, and choose a type:

| Type | Use it for | What you get |
|---|---|---|
| **HTTP** | websites, NAS panels, anything you open in a browser | `https://nas.example.com` |
| **TCP** | SSH, databases, Minecraft, remote desktop | `your-vps-ip:port` |
| **UDP** | game servers, WireGuard, DNS | `your-vps-ip:port` |

For an **HTTP** tunnel:

- **Local address / port** — where the service actually runs, e.g.
  `192.168.1.50` and `5000`.
- **Domains** — the name visitors will use, e.g. `nas.example.com`.
- **Enable HTTPS** — serve it on 443 as well.

For **TCP** or **UDP**, set the **remote port** instead: the port on the VPS
that will be forwarded to your service.

Tick **Enabled** and press **Save & Apply**.

### Step 3 — point your domain at the VPS

At your DNS provider, create an `A` record:

```
nas.example.com.   A   <your VPS IP>
```

Or a wildcard, so every name under it arrives without adding records one by
one:

```
*.example.com.     A   <your VPS IP>
```

Wait for it to propagate — usually a minute or two — then open
`http://nas.example.com`. You should see your service.

### Step 4 — get an HTTPS certificate

Go to **Certificates**, **Request a certificate**, enter the domain, and
submit. For a plain name like `nas.example.com` nothing else is needed: the
VPS answers the challenge itself, because the name already points at it.

For a **wildcard** (`*.example.com`) the certificate authority insists on a
DNS check, so you first add your DNS provider's API credentials under the
**DNS** tab. Supported: Aliyun, DNSPod, Huawei Cloud, Cloudflare, NameSilo,
PowerDNS and West.

Once issued, edit the tunnel, set **TLS handling** to *The remote server
handles HTTPS*, and choose the certificate. It is pushed to the VPS without
dropping any connections, and renews itself.

## Domain routing

Any number of tunnels share ports 80 and 443 on the same VPS; they are told
apart by the name in the request. Wildcards are supported, and a `*` stands
for **exactly one** level:

| Pattern | Matches | Does not match |
|---|---|---|
| `aaa.com` | `aaa.com` | any subdomain |
| `*.aaa.com` | `www.aaa.com`, `nas.aaa.com` | `x.bb.aaa.com` |
| `*.bb.aaa.com` | `x.bb.aaa.com` | `y.x.bb.aaa.com` |

An exact name always wins over a wildcard, so you can point `*.aaa.com` at one
tunnel and `shop.aaa.com` at another.

This is the same rule DNS and HTTPS certificates use: a `*.aaa.com`
certificate covers `www.aaa.com` but not `x.bb.aaa.com`. Matching it means a
visitor can never be routed to a tunnel whose certificate does not cover the
name they typed.

## Common setups

**A NAS with a web panel** — HTTP tunnel, local port `5000`, domain
`nas.example.com`, HTTPS on, certificate issued in step 4.

**SSH into your home machine** — TCP tunnel, local `192.168.1.10:22`, remote
port `2222`. Connect with `ssh -p 2222 user@your-vps-ip`.

**A game server** — UDP (or TCP, depending on the game), local port whatever
the server listens on, remote port the same so players do not have to be told
a different one.

**Several sites on one VPS** — one HTTP tunnel per site, each with its own
domain. They share port 443; nothing extra to configure.

## Bandwidth limits and traffic

Each tunnel can be capped, in its edit dialog:

- **Download limit** — KB/s toward visitors. `0` means no limit.
- **Upload limit** — KB/s from visitors.
- **Traffic cap** — total in MB. When reached, that tunnel stops accepting
  new connections. Useful on a VPS with a monthly allowance.

The **Status** page shows live up and down rates per tunnel, and daily totals
are kept for 400 days.

## Keeping it up to date

The **Status** page shows the version it is running. When a newer release
exists a button appears next to it; pressing it shows what changed and asks
you to confirm.

An update replaces everything — client, server binary, web interface,
translations — and restarts the service. If the new version fails to start,
the old one is put back automatically, so a bad release cannot leave the
router without tunnels.

You can also just re-run the installer, or `apk upgrade` / `opkg upgrade` if
you installed from a feed.

## If something is not working

### The server shows as disconnected

The most common cause is not a firewall — it is a **transparent proxy on the
router**. OpenClash, Passwall and ShellCrash all redirect outbound TCP, and
the tunnel's control connection gets swept up and sent through a proxy node
that cannot carry it.

The giveaway: the log says the connection succeeded and then immediately
closed, and the VPS's log shows nothing at all.

Two fixes, either works:

- Turn on **Skip the proxy** for that server, in its edit dialog.
- Or add a direct rule in your proxy's config, above the final catch-all:
  ```yaml
  - "IP-CIDR,<your-vps-ip>/32,DIRECT,no-resolve"
  ```

### The domain gives "no tunnel is serving this name"

The request reached the VPS, so DNS is right. Either the tunnel is not
enabled, or the name is not in its **Domains** list. Remember `*.example.com`
does not cover `a.b.example.com`.

### The certificate request fails

For a plain name, check the domain already points at the VPS — the authority
has to reach it there. For a wildcard, check the DNS credentials under the
**DNS** tab; the **Test** button says whether they work.

### The service records every visitor as the router

Turn on **Client IP** in the tunnel, then configure the service to trust it.
For nginx:

```nginx
listen 5000 proxy_protocol;
set_real_ip_from <your router's LAN address>;
real_ip_header proxy_protocol;
```

Configure the service **first** — a service that is not expecting the PROXY
protocol header will reject every request once you turn this on.

### DNS and certificate pages say they are unavailable

Those need SQLite, which has no build for MIPS routers. Tunnels are
unaffected; you can still use every tunnel type, just without certificate
management on the router.

## Removing it

```bash
apk del luci-app-openfrp openfrp
```

or with the installer:

```bash
sh install.sh --uninstall
```

On the VPS, the deploy page has a **Remove** action that takes the server side
off cleanly.

## For developers

Build, test and contribution notes live in
[docs/development.md](docs/development.md), and getting the package into the
OpenWrt feeds is [docs/openwrt-feed.md](docs/openwrt-feed.md). The short version:

```bash
make build       # both binaries, static, no CGO
make check       # vet, gofmt, race-enabled tests
make test-linux  # the suite on Linux, where splice(2) actually engages
```

Why it is quick, and the measurements behind that claim — including the cases
where it ties or loses — are in [docs/benchmark.md](docs/benchmark.md).

## Licence

MIT. See [LICENSE](LICENSE).
