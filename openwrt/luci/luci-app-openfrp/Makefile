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

PKG_VERSION:=1.1.2
PKG_RELEASE:=1
PKG_MAINTAINER:=zoefix <zony1937@gmail.com>
PKG_LICENSE:=MIT

# Inside the official luci feed the relative path resolves; from an
# out-of-tree feed it does not. Choosing between them keeps one Makefile valid
# in both places.
LUCI_MK:=$(wildcard ../../luci.mk)
ifeq ($(LUCI_MK),)
LUCI_MK:=$(TOPDIR)/feeds/luci/luci.mk
endif
include $(LUCI_MK)

# luci.mk supplies BuildPackage, the install rules, and one i18n subpackage
# per directory under po/. Nothing else to declare: root/ is copied to /, and
# htdocs/ to /www/.
