#!/bin/sh

set -eu

VERSION="${VERSION:?VERSION is required}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
DATE="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
PLATFORMS="${PLATFORMS:-linux/amd64 linux/arm64}"
DIST="${DIST:-dist}"

MODULE=github.com/zoefix/openfrp
LDFLAGS="-s -w \
	-X $MODULE/internal/version.Version=$VERSION \
	-X $MODULE/internal/version.Commit=$COMMIT \
	-X $MODULE/internal/version.Date=$DATE"

LUCI=openwrt/luci/luci-app-openfrp
RESOURCES=$LUCI/htdocs/luci-static/resources

rm -rf "$DIST"
mkdir -p "$DIST"

CATALOGUES="$DIST/.lmo"
mkdir -p "$CATALOGUES"
for pair in zh_Hans:zh-cn zh_Hant:zh-tw ja:ja; do
	src="${pair%%:*}"
	out="${pair##*:}"
	go run ./tools/luci-i18n compile "$LUCI/po/$src/openfrp.po" \
		"$CATALOGUES/openfrp.$out.lmo" >/dev/null
done

for platform in $PLATFORMS; do
	os="${platform%/*}"
	arch="${platform#*/}"

	root="$DIST/.root-$os-$arch"
	rm -rf "$root"

	mkdir -p "$root/etc/init.d" \
		"$root/usr/bin" \
		"$root/usr/lib/openfrp" \
		"$root/usr/libexec/openfrp" \
		"$root/usr/share/luci/menu.d" \
		"$root/usr/share/rpcd/acl.d" \
		"$root/usr/share/rpcd/ucode" \
		"$root/usr/lib/lua/luci/i18n" \
		"$root/www/luci-static/resources/openfrp" \
		"$root/www/luci-static/resources/view/openfrp"

	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
		go build -trimpath -ldflags "$LDFLAGS" -o "$root/usr/bin/openfrpc" ./cmd/openfrpc
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
		go build -trimpath -ldflags "$LDFLAGS" -o "$root/usr/lib/openfrp/openfrps" ./cmd/openfrps

	cp openwrt/net/openfrp/files/openfrp-render "$root/usr/libexec/openfrp/render"
	cp "$LUCI/root/usr/libexec/openfrp/job" "$root/usr/libexec/openfrp/job"
	chmod 0755 "$root/usr/libexec/openfrp/render" "$root/usr/libexec/openfrp/job"

	cp "$LUCI/root/usr/share/rpcd/ucode/openfrp.uc" "$root/usr/share/rpcd/ucode/openfrp.uc"

	# Without the menu entry there is no OpenFrp tab, and without the ACL every
	# ubus call is refused. A bundle carrying only the views installs an
	# interface that cannot be reached or cannot talk to anything.
	cp "$LUCI/root/usr/share/luci/menu.d/luci-app-openfrp.json" \
		"$root/usr/share/luci/menu.d/luci-app-openfrp.json"
	cp "$LUCI/root/usr/share/rpcd/acl.d/luci-app-openfrp.json" \
		"$root/usr/share/rpcd/acl.d/luci-app-openfrp.json"

	cp openwrt/net/openfrp/files/openfrp.init "$root/etc/init.d/openfrp"
	cp openwrt/net/openfrp/files/openfrp-cloudflared.init \
		"$root/etc/init.d/openfrp-cloudflared"
	chmod 0755 "$root/etc/init.d/openfrp" "$root/etc/init.d/openfrp-cloudflared"
	cp "$CATALOGUES"/openfrp.*.lmo "$root/usr/lib/lua/luci/i18n/"
	cp "$RESOURCES"/openfrp/*.js "$root/www/luci-static/resources/openfrp/"
	cp "$RESOURCES"/view/openfrp/*.js "$root/www/luci-static/resources/view/openfrp/"

	bundle="openfrp-$VERSION-$os-$arch.tar.gz"
	TAR_FLAGS=""
	tar --no-xattrs --version >/dev/null 2>&1 && TAR_FLAGS="--no-xattrs"
	# Whoever built this is not who should own it once installed. Without
	# these, tar records the build account's uid and a root extract restores
	# it, leaving /usr/bin/openfrpc owned by a number that means nothing on
	# the router — and everything to a local account that happens to match.
	tar --owner=0 --group=0 --version >/dev/null 2>&1 &&
		TAR_FLAGS="$TAR_FLAGS --owner=0 --group=0"
	COPYFILE_DISABLE=1 tar $TAR_FLAGS -czf "$DIST/$bundle" -C "$root" .
	rm -rf "$root"
	echo "$DIST/$bundle"
done

rm -rf "$CATALOGUES"

(
	cd "$DIST"
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum ./*.tar.gz | sed 's| \./| |' > checksums.txt
	else
		shasum -a 256 ./*.tar.gz | sed 's| \./| |' > checksums.txt
	fi
	cat checksums.txt
)
