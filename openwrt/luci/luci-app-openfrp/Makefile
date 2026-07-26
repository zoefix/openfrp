#
# LuCI interface for OpenFrp.
#
# Copyright (C) 2026 zoefix
# Licensed under the MIT License.
#
include $(TOPDIR)/rules.mk

LUCI_TITLE:=LuCI support for OpenFrp
LUCI_DESCRIPTION:=Configure NAT traversal tunnels, wildcard domain routing, \
	and one-click server provisioning over SSH.
LUCI_DEPENDS:=+luci-base +openfrp +rpcd-mod-file +rpcd-mod-ucode
LUCI_PKGARCH:=all

PKG_VERSION:=0.2.0
PKG_RELEASE:=1
PKG_MAINTAINER:=zoefix <zony1937@gmail.com>
PKG_LICENSE:=MIT

# The absolute path is required from an out-of-tree feed.
include $(TOPDIR)/feeds/luci/luci.mk

# luci.mk supplies BuildPackage, the install rules, and one i18n subpackage
# per directory under po/. Nothing else to declare: root/ is copied to /, and
# htdocs/ to /www/.
