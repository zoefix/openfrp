#!/bin/sh

set -eu

REPO="${OPENFRP_REPO:-zoefix/openfrp}"
API="${OPENFRP_API:-https://api.github.com}"
MANIFEST="/usr/lib/openfrp/installed.list"

say() { printf '%s\n' "$*"; }
step() { printf '==> %s\n' "$*"; }
die() { printf 'install: %s\n' "$*" >&2; exit 1; }

usage() {
	cat <<'EOF'
usage: install.sh [options]

  --uninstall        remove OpenFrp and its interface, keeping settings
  --purge            remove OpenFrp and its settings, certificates and history
  --version VERSION  install a specific release, e.g. v0.4.0
  --lang LANG        also install a translation: zh-cn, zh-tw or ja
  --help             this

Environment:
  OPENFRP_REPO       the GitHub repository (default zoefix/openfrp)
  OPENFRP_API        a GitHub API mirror, if github.com is unreachable
EOF
}

WANT_VERSION=""
WANT_LANG=""
UNINSTALL=0
PURGE=0

while [ $# -gt 0 ]; do
	case "$1" in
		--uninstall) UNINSTALL=1 ;;
		--purge) UNINSTALL=1; PURGE=1 ;;
		--version) shift; WANT_VERSION="${1:-}" ;;
		--lang) shift; WANT_LANG="${1:-}" ;;
		--help|-h) usage; exit 0 ;;
		*) die "unknown option $1 (try --help)" ;;
	esac
	shift
done

detect_pm() {
	if command -v apk >/dev/null 2>&1 && apk --version 2>&1 | grep -qi 'apk-tools'; then
		echo apk
	elif command -v opkg >/dev/null 2>&1; then
		echo opkg
	else
		die "neither apk nor opkg was found; this script installs on OpenWrt"
	fi
}

# detect_arch maps OpenWrt's package architecture onto the one the release is
# built for. The Go name is what the release asset is called; the OpenWrt name
# is what the router calls itself, and the two do not always agree.
detect_arch() {
	arch=""
	if [ -r /etc/os-release ]; then
		. /etc/os-release 2>/dev/null || true
		arch="${OPENWRT_ARCH:-}"
	fi
	[ -n "$arch" ] || arch="$(uname -m 2>/dev/null || echo unknown)"

	case "$arch" in
		x86_64|amd64) echo amd64 ;;
		aarch64*|arm64*) echo arm64 ;;
		armv7*|armv6*|arm_*) echo arm ;;
		mipsel*|mipsle*) echo mipsle ;;
		mips*) echo mips ;;
		i386|i486|i586|i686|x86) echo 386 ;;
		*) die "unsupported architecture: $arch" ;;
	esac
}

fetch() {
	url="$1"
	out="$2"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --connect-timeout 20 -o "$out" "$url"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -T 20 -O "$out" "$url"
	else
		die "neither curl nor wget is available"
	fi
}

fetch_stdout() {
	url="$1"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --connect-timeout 20 "$url"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -T 20 -O - "$url"
	else
		die "neither curl nor wget is available"
	fi
}

json_field() {
	sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | head -n 1
}

uninstall() {
	pm="$(detect_pm)"
	step "removing OpenFrp with $pm"

	for script in /etc/init.d/openfrp-cloudflared /etc/init.d/openfrp; do
		if [ -x "$script" ]; then
			"$script" stop >/dev/null 2>&1 || true
			"$script" disable >/dev/null 2>&1 || true
		fi
	done

	# Where a package manager owns these, removing the package is the only way
	# its database stays honest. Where this script put them there, it did not
	# register anything, so nothing would be removed by asking.
	removed=0
	for pkg in luci-i18n-openfrp-zh-cn luci-i18n-openfrp-zh-tw luci-i18n-openfrp-ja \
		luci-app-openfrp openfrp; do
		case "$pm" in
			apk)
				apk info -e "$pkg" >/dev/null 2>&1 || continue
				apk del "$pkg" >/dev/null 2>&1 && removed=$((removed + 1))
				;;
			opkg)
				opkg status "$pkg" 2>/dev/null | grep -q . || continue
				opkg remove "$pkg" >/dev/null 2>&1 && removed=$((removed + 1))
				;;
		esac
	done

	if [ "$removed" -gt 0 ]; then
		say "Removed $removed package(s) with $pm."
	fi

	# What this script installed, it recorded. Guessing at the file list
	# instead would either miss files a later version added or delete ones it
	# never owned.
	if [ -f "$MANIFEST" ]; then
		count=0
		while IFS= read -r file; do
			[ -n "$file" ] || continue
			case "$file" in
				/etc/init.d/openfrp|/etc/init.d/openfrp-cloudflared| \
				/usr/bin/openfrpc|/usr/lib/openfrp/*|/usr/libexec/openfrp/*| \
				/usr/share/luci/menu.d/luci-app-openfrp.json| \
				/usr/share/rpcd/acl.d/luci-app-openfrp.json| \
				/usr/share/rpcd/ucode/openfrp.uc|/usr/lib/lua/luci/i18n/openfrp.*| \
				/www/luci-static/resources/openfrp/*|/www/luci-static/resources/view/openfrp/*)
					rm -f "$file" && count=$((count + 1))
					;;
			esac
		done < "$MANIFEST"
		rm -f "$MANIFEST"
		rmdir /usr/lib/openfrp /usr/libexec/openfrp \
			/www/luci-static/resources/openfrp \
			/www/luci-static/resources/view/openfrp 2>/dev/null || true
		say "Removed $count installed file(s)."
	elif [ "$removed" = 0 ]; then
		say "Nothing to remove: no package owns OpenFrp and no install manifest was found."
	fi

	rm -f /tmp/luci-indexcache 2>/dev/null || true
	[ -x /etc/init.d/rpcd ] && /etc/init.d/rpcd restart >/dev/null 2>&1 || true

	if [ "$PURGE" = 1 ]; then
		rm -f /etc/config/openfrp
		rm -rf /etc/openfrp
		rm -f /var/run/openfrp/update.json /var/run/openfrp/stats.json
		say "Purged: settings, certificates, DNS accounts and traffic history are gone."
		return 0
	fi

	# Kept on purpose, and named so it is obvious what survived. A package
	# manager keeps its conffiles too, and reinstalling onto your own tunnels
	# is the common case; losing them to a reinstall would not be.
	say "Kept your settings. Reinstalling will pick them up again:"
	say "  /etc/config/openfrp        servers, tunnels, tokens"
	[ -e /etc/openfrp/openfrp.db ] &&
		say "  /etc/openfrp/openfrp.db    certificates, DNS accounts, traffic history"
	[ -d /etc/openfrp/cloudflared ] &&
		say "  /etc/openfrp/cloudflared   cloudflared credentials"
	say "Run with --purge to remove those as well."
}

if [ "$UNINSTALL" = 1 ]; then
	uninstall
	exit 0
fi

[ "$(id -u)" = 0 ] || die "run this as root"

PM="$(detect_pm)"
ARCH="$(detect_arch)"
step "OpenWrt with $PM, architecture $ARCH"

step "asking $REPO for a release"
if [ -n "$WANT_VERSION" ]; then
	RELEASE_JSON="$(fetch_stdout "$API/repos/$REPO/releases/tags/$WANT_VERSION")" ||
		die "no release tagged $WANT_VERSION"
else
	RELEASE_JSON="$(fetch_stdout "$API/repos/$REPO/releases/latest")" ||
		die "could not reach $API — set OPENFRP_API to a mirror if github.com is blocked"
fi

TAG="$(printf '%s' "$RELEASE_JSON" | json_field tag_name)"
[ -n "$TAG" ] || die "the repository has published no releases yet"
VERSION="${TAG#v}"
step "installing $TAG"

BUNDLE="openfrp-$VERSION-linux-$ARCH.tar.gz"
BUNDLE_URL="$(printf '%s' "$RELEASE_JSON" | tr ',' '\n' |
	grep 'browser_download_url' | grep -F "$BUNDLE" | json_field browser_download_url)"
SUMS_URL="$(printf '%s' "$RELEASE_JSON" | tr ',' '\n' |
	grep 'browser_download_url' | grep -F 'checksums.txt' | json_field browser_download_url)"

[ -n "$BUNDLE_URL" ] || die "$TAG has no build for $ARCH"
[ -n "$SUMS_URL" ] || die "$TAG publishes no checksums.txt; refusing to install an unverifiable download"

WORK="$(mktemp -d /tmp/openfrp-install.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT INT TERM

step "downloading $BUNDLE"
fetch "$BUNDLE_URL" "$WORK/$BUNDLE"
fetch "$SUMS_URL" "$WORK/checksums.txt"

# The expected hash comes from the release, so a bundle that arrived intact
# from somewhere else is still refused.
WANT="$(grep -F " $BUNDLE" "$WORK/checksums.txt" | awk '{print $1}' | head -n 1)"
[ -n "$WANT" ] || die "$BUNDLE is not listed in checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
	GOT="$(sha256sum "$WORK/$BUNDLE" | awk '{print $1}')"
else
	die "sha256sum is missing, so the download cannot be verified"
fi

[ "$GOT" = "$WANT" ] || die "checksum mismatch: got $GOT, expected $WANT"
step "checksum verified"

step "unpacking"
mkdir -p "$WORK/root"
tar -xzf "$WORK/$BUNDLE" -C "$WORK/root"

[ -x "$WORK/root/usr/bin/openfrpc" ] || die "the bundle has no usable client"

# Run it before replacing anything, so a build for the wrong architecture is
# caught while the working install is still in place.
"$WORK/root/usr/bin/openfrpc" version >/dev/null 2>&1 ||
	die "the downloaded client does not run on this machine; wrong architecture?"

step "installing dependencies"
case "$PM" in
	apk)
		apk update >/dev/null 2>&1 || true
		apk add jshn rpcd-mod-file rpcd-mod-ucode luci-base >/dev/null 2>&1 || true
		;;
	opkg)
		opkg update >/dev/null 2>&1 || true
		opkg install jshn rpcd-mod-file rpcd-mod-ucode luci-base >/dev/null 2>&1 || true
		;;
esac

RUNNING=0
if [ -x /etc/init.d/openfrp ]; then
	/etc/init.d/openfrp running >/dev/null 2>&1 && RUNNING=1
	/etc/init.d/openfrp stop >/dev/null 2>&1 || true
fi

step "installing files"
# --no-same-owner where tar has it: root extracting an archive restores the
# uid it records, which is the build account's, not this machine's root.
if tar --help 2>&1 | grep -q -- --no-same-owner; then
	tar --no-same-owner -xzf "$WORK/$BUNDLE" -C /
else
	tar -xzf "$WORK/$BUNDLE" -C /
fi

# tar as root restores the archive's modes, but not every tar does when a
# umask is set, and a 0600 interface file is served as 403 rather than as a
# broken page. Cheap to state outright.
chmod 0755 /usr/bin/openfrpc /usr/libexec/openfrp/* 2>/dev/null || true
[ -f /usr/lib/openfrp/openfrps ] && chmod 0755 /usr/lib/openfrp/openfrps
chmod 0755 /usr/lib/openfrp /usr/libexec/openfrp \
	/www/luci-static/resources/openfrp \
	/www/luci-static/resources/view/openfrp 2>/dev/null || true
chmod 0644 /usr/share/rpcd/ucode/openfrp.uc 2>/dev/null || true
chmod 0644 /usr/share/luci/menu.d/luci-app-openfrp.json 2>/dev/null || true
chmod 0644 /usr/share/rpcd/acl.d/luci-app-openfrp.json 2>/dev/null || true
chmod 0644 /usr/lib/lua/luci/i18n/openfrp.*.lmo 2>/dev/null || true
chmod 0644 /www/luci-static/resources/openfrp/*.js 2>/dev/null || true
chmod 0644 /www/luci-static/resources/view/openfrp/*.js 2>/dev/null || true

# Stated rather than inherited, for the same reason as the modes.
chown -R root:root /usr/bin/openfrpc /usr/lib/openfrp /usr/libexec/openfrp \
	/etc/init.d/openfrp /etc/init.d/openfrp-cloudflared \
	/usr/share/luci/menu.d/luci-app-openfrp.json \
	/usr/share/rpcd/acl.d/luci-app-openfrp.json \
	/usr/share/rpcd/ucode/openfrp.uc \
	/www/luci-static/resources/openfrp \
	/www/luci-static/resources/view/openfrp 2>/dev/null || true
for lmo in /usr/lib/lua/luci/i18n/openfrp.*.lmo; do
	[ -e "$lmo" ] && chown root:root "$lmo" 2>/dev/null || true
done

# Record what was installed so --uninstall removes exactly this and nothing
# else. A hardcoded list would miss whatever a later release adds.
mkdir -p "$(dirname "$MANIFEST")"
tar -tzf "$WORK/$BUNDLE" |
	sed -e 's|^\./|/|' -e 's|^\([^/]\)|/\1|' |
	grep -v '/$' > "$MANIFEST"

if [ ! -f /etc/config/openfrp ]; then
	step "writing the default configuration"
	mkdir -p /etc/config
	cat > /etc/config/openfrp <<'EOF'
config global 'global'
	option enabled '0'
	option log_level 'info'

config server 'server'
	option addr ''
	option port '7000'
	option token ''
EOF
fi

for script in /etc/init.d/openfrp /etc/init.d/openfrp-cloudflared; do
	[ -f "$script" ] && chmod 0755 "$script"
done

if [ -x /etc/init.d/openfrp ]; then
	/etc/init.d/openfrp enable >/dev/null 2>&1 || true

	# Start when the configuration says the service is wanted, not merely when
	# it happened to be running a moment ago. After an uninstall nothing is
	# running, so the old test left a fresh install enabled and stopped —
	# which the status page reports, accurately and unhelpfully, as "enabled
	# but not running".
	WANTED="$(uci -q get openfrp.global.enabled 2>/dev/null || echo 0)"
	if [ "$WANTED" = "1" ] || [ "$RUNNING" = 1 ]; then
		/etc/init.d/openfrp start >/dev/null 2>&1 || true
		step "started the service"
	else
		step "service installed but left stopped; enable OpenFrp in LuCI to start it"
	fi
fi

[ -x /etc/init.d/rpcd ] && /etc/init.d/rpcd restart >/dev/null 2>&1 || true
rm -f /tmp/luci-indexcache /tmp/luci-modulecache/* 2>/dev/null || true

if [ -n "$WANT_LANG" ]; then
	case "$PM" in
		apk) apk add "luci-i18n-openfrp-$WANT_LANG" >/dev/null 2>&1 ||
			say "note: luci-i18n-openfrp-$WANT_LANG is not in your feed; the bundle already carries the catalogue" ;;
		opkg) opkg install "luci-i18n-openfrp-$WANT_LANG" >/dev/null 2>&1 ||
			say "note: luci-i18n-openfrp-$WANT_LANG is not in your feed; the bundle already carries the catalogue" ;;
	esac
fi

INSTALLED="$(/usr/bin/openfrpc version 2>/dev/null || echo unknown)"
say ""
say "Installed: $INSTALLED"
say ""
say "Open LuCI and go to Services -> OpenFrp."
say "If the menu is not there, log out and back in."
