// Command luci-i18n manages the LuCI app's translations without a buildroot.
//
// LuCI's own toolchain lives inside OpenWrt: i18n-scan.pl extracts strings and
// a C po2lmo compiles catalogues. Both are build-time tools, so translations
// normally cannot be produced or checked outside a buildroot. This is a
// standalone equivalent of the parts this project needs.
//
//	luci-i18n extract <dir>...            scan JS for _() and write a .pot
//	luci-i18n compile <in.po> <out.lmo>   build a binary catalogue
//	luci-i18n dump    <in.lmo>            decode one, for inspection
//	luci-i18n lookup  <in.lmo> <msgid>... resolve strings against one
//
// The decoder exists because of how the format was verified: rather than
// trusting a reading of the spec, it was run against a stock .lmo from a live
// router, and `lookup` was used to confirm that hashing a known English string
// finds its translation in a catalogue LuCI itself produced. Only then was the
// encoder trusted. A hash that is merely close compiles a catalogue that loads
// without error and translates nothing.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "extract":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: luci-i18n extract <out.pot> <dir>...")
			os.Exit(2)
		}
		err = extract(os.Args[2], os.Args[3:])

	case "compile":
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: luci-i18n compile <in.po> <out.lmo>")
			os.Exit(2)
		}
		err = compile(os.Args[2], os.Args[3])

	case "dump":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: luci-i18n dump <in.lmo>")
			os.Exit(2)
		}
		err = dump(os.Args[2])

	case "lookup":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: luci-i18n lookup <in.lmo> <msgid>...")
			os.Exit(2)
		}
		err = lookup(os.Args[2], os.Args[3:])

	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "luci-i18n:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  luci-i18n extract <out.pot> <dir>...
  luci-i18n compile <in.po> <out.lmo>
  luci-i18n dump    <in.lmo>
  luci-i18n lookup  <in.lmo> <msgid>...
`)
}
