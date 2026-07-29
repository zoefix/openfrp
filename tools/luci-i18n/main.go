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
