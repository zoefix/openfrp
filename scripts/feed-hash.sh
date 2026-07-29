#!/bin/sh

set -eu

TAG="${1:-}"
[ -n "$TAG" ] || {
	echo "usage: feed-hash.sh <tag>      e.g. feed-hash.sh v0.4.0" >&2
	echo "" >&2
	echo "Downloads the tagged source tarball GitHub serves and prints the" >&2
	echo "PKG_HASH line for openwrt/net/openfrp/Makefile." >&2
	exit 2
}

REPO="${OPENFRP_REPO:-zoefix/openfrp}"
URL="https://codeload.github.com/$REPO/tar.gz/$TAG"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT INT TERM

echo "fetching $URL" >&2
if command -v curl >/dev/null 2>&1; then
	curl -fsSL -o "$WORK/src.tar.gz" "$URL"
else
	wget -q -O "$WORK/src.tar.gz" "$URL"
fi

if command -v sha256sum >/dev/null 2>&1; then
	HASH="$(sha256sum "$WORK/src.tar.gz" | awk '{print $1}')"
else
	HASH="$(shasum -a 256 "$WORK/src.tar.gz" | awk '{print $1}')"
fi

echo "PKG_HASH:=$HASH"

MAKEFILE=openwrt/net/openfrp/Makefile
if [ -w "$MAKEFILE" ]; then
	sed -i.bak "s|^PKG_HASH:=.*|PKG_HASH:=$HASH|" "$MAKEFILE"
	rm -f "$MAKEFILE.bak"
	echo "updated $MAKEFILE" >&2
fi
