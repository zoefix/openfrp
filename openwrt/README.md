# OpenWrt feed

Two packages:

| | |
|---|---|
| `openfrp` | The client daemon, UCI schema, procd service |
| `luci-app-openfrp` | Web UI, rpcd backend, zh_Hans translation |

## Installing the feed

From an OpenWrt buildroot or SDK:

```bash
echo "src-git openfrp https://github.com/zoefix/openfrp.git" >> feeds.conf
./scripts/feeds update openfrp
./scripts/feeds install -a -p openfrp
```

Then enable both under `Network → Web Servers/Proxies` and `LuCI → Applications`
in `make menuconfig`, and build:

```bash
make package/openfrp/compile V=s
make package/luci-app-openfrp/compile V=s
```

## Package format

**The target is OpenWrt 25.12, which uses `apk`. `.ipk` will not install
there.** The Makefiles are format-agnostic — buildroot emits whatever the
branch uses — so the format is decided entirely by which SDK you build with:

| SDK branch | Output |
|---|---|
| 25.12 | `.apk` — the primary target |
| 24.10 | `.ipk` — subject to its Go toolchain being new enough |
| 23.05 | `.ipk` — unlikely to build; see below |

The constraint is Go. Each branch pins a toolchain version in
`golang-values.mk`, and this project needs a recent one for
`modernc.org/sqlite` and `lego`. Keep `go.mod`'s `go` directive at the lowest
version the dependencies tolerate rather than raising it for convenience — that
directive is what closes off the older branches.

## Notes for anyone editing these files

**The include paths must be absolute.** An out-of-tree feed has to use
`$(TOPDIR)/feeds/packages/lang/golang/golang-package.mk` and
`$(TOPDIR)/feeds/luci/luci.mk`. The relative forms used inside the official
feeds only resolve from within those trees.

**Every shell script is POSIX sh.** The target shell is busybox ash. There is
no bash and no coreutils behind it — `nproc`, for example, does not exist on
the test router.

**rpcd kills any call past 30 seconds**, and uhttpd caps a CGI request at 60.
Anything slower than that — server provisioning, certificate issuance — has to
go through `job_start` / `job_status`, which detaches a worker and polls it.
Never make one of those a synchronous rpcd method.

**Changing an ACL requires `/etc/init.d/rpcd restart`.** Without it the new
permissions are ignored and every call fails with a bare "Access denied" and no
explanation.

**`root/` is copied verbatim to `/`** and `htdocs/` to `/www/`. `luci.mk`
handles it; there is nothing to declare in the Makefile.

**The daemon rejects unknown config keys.** `files/openfrp-render` translates
UCI into the JSON the daemon reads, and every key it emits must match
`internal/config` exactly. That strictness is deliberate: a typo should fail
loudly at startup instead of silently disabling the setting it was meant to
apply.

## Layout

```
net/openfrp/
  Makefile
  files/
    openfrp.config      /etc/config/openfrp
    openfrp.init        procd service
    openfrp-render      UCI → JSON, via jshn
    openfrp.defaults    one-shot install hook

luci/luci-app-openfrp/
  Makefile
  htdocs/luci-static/resources/view/openfrp/
    status.js  tunnels.js  server.js
  root/usr/share/luci/menu.d/luci-app-openfrp.json
  root/usr/share/rpcd/acl.d/luci-app-openfrp.json
  root/usr/share/rpcd/ucode/openfrp.uc
  root/usr/libexec/openfrp/job          detached worker
  po/zh_Hans/openfrp.po
```
