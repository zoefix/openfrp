# Getting into the OpenWrt package feeds

Two packages go to two different repositories:

| Package | Repository | Path |
|---|---|---|
| `openfrp` | [openwrt/packages](https://github.com/openwrt/packages) | `net/openfrp/` |
| `luci-app-openfrp` | [openwrt/luci](https://github.com/openwrt/luci) | `applications/luci-app-openfrp/` |

Both Makefiles in this repository are already written to build in either
place. The include of `golang-package.mk` and `luci.mk` picks the relative
path when the package sits inside a feed and the absolute one when it does
not, so what gets submitted upstream is what was built and tested here.

## apk and opkg

Nothing in the Makefiles chooses between them. OpenWrt's build system emits
`.apk` on 24.10 and later and `.ipk` before that, from the same source. What
the package has to do is stay compatible with both, which means:

- `PKG_VERSION` holds only digits and dots, and `PKG_RELEASE` is a plain
  integer. apk is stricter about version strings than opkg was.
- `conffiles` marks `/etc/config/openfrp`, so neither manager overwrites a
  configuration on upgrade.
- `postinst` and `prerm` guard on `IPKG_INSTROOT`, which both managers set
  when they are populating an image rather than a running system. Without the
  guard the scripts try to start services inside the build root.

## Before submitting

### 1. Tag a release

The feed builds from a tagged tarball, not from a branch. Tracking a branch
would make two builds of the same package version produce different binaries,
which is the thing a package version exists to prevent.

```bash
git tag v0.4.0 && git push --tags
```

### 2. Fill in the hash

```bash
./scripts/feed-hash.sh v0.4.0
```

It downloads the tarball GitHub serves for that tag and rewrites `PKG_HASH`
in `openwrt/net/openfrp/Makefile`. Commit that.

`PKG_VERSION` in both Makefiles has to match the tag, and
`internal/version/version.go` has to agree with both — the release workflow
already refuses to publish when the tag and the source disagree.

### 3. Build it the way the feed will

Submissions are expected to have been built in a real buildroot, for more than
one architecture. From an OpenWrt checkout:

```bash
echo "src-link openfrp /path/to/this/repo/openwrt" >> feeds.conf.default
./scripts/feeds update openfrp
./scripts/feeds install -a -p openfrp

make menuconfig     # Network -> Web Servers/Proxies -> openfrp
make package/openfrp/compile V=s
make package/luci-app-openfrp/compile V=s
```

Build for at least one big-endian target as well — mips is where assumptions
about alignment and word size surface.

### 4. Open the pull requests

`openfrp` first: the LuCI app depends on it, and a `luci-app-*` whose
dependency is not in a feed yet cannot be built by their CI.

Each pull request wants one commit per package, with a message in the form
the feeds use:

```
openfrp: add new package

OpenFrp exposes services behind NAT through a public server, with
wildcard domain routing, DNS management and ACME certificates.

Signed-off-by: Your Name <you@example.com>
```

The sign-off is required. `git commit -s` adds it.

Their CI builds the package for several architectures and runs a Makefile
linter. Common things it objects to:

- A `PKG_HASH` that does not match what the URL serves.
- A missing `PKG_MAINTAINER`, `PKG_LICENSE` or `PKG_LICENSE_FILES`.
- `PKG_RELEASE` not incremented when a package is changed but its version is
  not.
- Files installed outside the paths a package may own.

## After it is in

Each new release needs a follow-up pull request bumping `PKG_VERSION` and
`PKG_HASH`, with `PKG_RELEASE` reset to 1. Bump `PKG_RELEASE` instead when
only the packaging changes.

The update button on the status page notices when a package manager owns
these files and upgrades through it instead of replacing them underneath it,
so a feed install stays describable by `apk info` / `opkg status`. The
in-place path is only taken where nothing owns them.
