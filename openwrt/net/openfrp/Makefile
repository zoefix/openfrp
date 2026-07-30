#
# OpenFrp client for OpenWrt.
#
# Copyright (C) 2026 zoefix
# Licensed under the MIT License. See /LICENSE for details.
#
# Builds unchanged whether this sits in the official packages feed or in this
# repository used as an out-of-tree feed; see the golang-package.mk include.
#
include $(TOPDIR)/rules.mk

PKG_NAME:=openfrp
PKG_VERSION:=1.1.2
PKG_RELEASE:=1

PKG_SOURCE:=$(PKG_NAME)-$(PKG_VERSION).tar.gz
PKG_SOURCE_URL:=https://codeload.github.com/zoefix/openfrp/tar.gz/v$(PKG_VERSION)?
# skip is acceptable for a local feed and not for the official one. Routers
# get releases through scripts/install.sh, which verifies its download against
# the checksums the release publishes; this path exists for building OpenFrp
# into a firmware image.
PKG_HASH:=skip
PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)

PKG_MAINTAINER:=zoefix <zony1937@gmail.com>
PKG_LICENSE:=MIT
PKG_LICENSE_FILES:=LICENSE

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=github.com/zoefix/openfrp
# Both binaries. openfrps is not run on the router — it is the payload the SSH
# provisioner uploads, so a deployment works with no outbound internet on the
# server and installs bytes this router checksummed. It is built for the
# router's own architecture, so it only fits a server of the same kind; the
# deployer checks the ELF machine type and falls back to a download when it
# does not match.
GO_PKG_BUILD_PKG:=github.com/zoefix/openfrp/cmd/...
GO_PKG_LDFLAGS:=-s -w
GO_PKG_LDFLAGS_X:=github.com/zoefix/openfrp/internal/version.Version=$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk

# Inside the official feed the relative path resolves; from an out-of-tree
# feed it does not, and the absolute one is needed. Choosing between them here
# keeps one Makefile valid in both places, so what is submitted upstream is
# what was built and tested locally.
GOLANG_PACKAGE_MK:=$(wildcard ../../lang/golang/golang-package.mk)
ifeq ($(GOLANG_PACKAGE_MK),)
GOLANG_PACKAGE_MK:=$(TOPDIR)/feeds/packages/lang/golang/golang-package.mk
endif
include $(GOLANG_PACKAGE_MK)

define Package/openfrp
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=Web Servers/Proxies
  TITLE:=OpenFrp client — NAT traversal with wildcard domain routing
  URL:=https://github.com/zoefix/openfrp
  DEPENDS:=$(GO_ARCH_DEPENDS) +jshn
endef

define Package/openfrp/description
  OpenFrp exposes services behind NAT through a public server.

  Each work connection is its own TCP connection rather than a multiplexed
  stream, which keeps the kernel splice(2) path reachable and stops one lost
  packet from stalling every tunnel at once.

  Domain-routed tunnels share ports 80 and 443. A "*" label matches exactly
  one level and may appear at any depth, matching DNS and TLS certificate
  scope rather than frp's broader rule.
endef

define Package/openfrp/conffiles
/etc/config/openfrp
endef

define Package/openfrp/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(GO_PKG_BUILD_BIN_DIR)/openfrpc $(1)/usr/bin/openfrpc

	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/openfrp.config $(1)/etc/config/openfrp

	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/openfrp.init $(1)/etc/init.d/openfrp
	# cloudflared is supervised by its own service. It exits by itself when its
	# last connection to Cloudflare drops, and sharing a service with the client
	# made that exit take every other tunnel down with it.
	$(INSTALL_BIN) ./files/openfrp-cloudflared.init $(1)/etc/init.d/openfrp-cloudflared

	$(INSTALL_DIR) $(1)/etc/uci-defaults
	$(INSTALL_DATA) ./files/openfrp.defaults $(1)/etc/uci-defaults/90-openfrp
	$(INSTALL_DATA) ./files/openfrp.migrate $(1)/etc/uci-defaults/91-openfrp-servers

	$(INSTALL_DIR) $(1)/usr/libexec/openfrp
	$(INSTALL_BIN) ./files/openfrp-render $(1)/usr/libexec/openfrp/render

	$(INSTALL_DIR) $(1)/usr/lib/openfrp
	$(INSTALL_BIN) $(GO_PKG_BUILD_BIN_DIR)/openfrps $(1)/usr/lib/openfrp/openfrps
endef

define Package/openfrp/postinst
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] || {
	/etc/init.d/openfrp enable >/dev/null 2>&1
	# Enabled even where no Cloudflare tunnel is configured: it starts nothing
	# in that case, and being enabled is what registers its reload trigger, so
	# a tunnel set up later starts without anyone remembering this step.
	/etc/init.d/openfrp-cloudflared enable >/dev/null 2>&1
	exit 0
}
endef

define Package/openfrp/prerm
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] || {
	/etc/init.d/openfrp-cloudflared stop >/dev/null 2>&1
	/etc/init.d/openfrp-cloudflared disable >/dev/null 2>&1
	/etc/init.d/openfrp stop >/dev/null 2>&1
	/etc/init.d/openfrp disable >/dev/null 2>&1
}
exit 0
endef

$(eval $(call GoBinPackage,openfrp))
$(eval $(call BuildPackage,openfrp))
