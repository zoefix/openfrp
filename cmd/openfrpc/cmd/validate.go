package cmd

import (
	"context"
	"flag"
	"fmt"

	"github.com/zoefix/openfrp/internal/config"
)

func init() {
	register(&Command{
		Name:    "validate",
		Summary: "check a configuration file and exit",
		Run:     runValidate,
	})
}

func runValidate(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	configPath := fs.String("c", "/var/etc/openfrp.json", "path to the configuration file")
	quiet := fs.Bool("q", false, "report only errors")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.LoadClient(*configPath)
	if err != nil {
		return err
	}

	if *quiet {
		return nil
	}

	enabled := cfg.EnabledTunnels()
	fmt.Printf("configuration is valid: %s\n", *configPath)
	servers := cfg.Upstreams()
	for _, server := range servers {
		tunnels := cfg.TunnelsFor(server.Name)
		fmt.Printf("  server    %-12s %s:%d  %s, mux=%v, pool=%d  (%d tunnel(s))\n",
			server.Name, server.Addr, server.Port,
			server.Transport.Protocol, server.Transport.Mux,
			server.Transport.PoolCount, len(tunnels))
	}
	fmt.Printf("  tunnels   %d enabled of %d\n", len(enabled), len(cfg.Tunnels))

	for _, tunnel := range enabled {
		switch {
		case len(tunnel.Domains) > 0:
			fmt.Printf("    %-16s %-6s %s:%d  ← %v\n", tunnel.Name, tunnel.Type,
				tunnel.LocalIP, tunnel.LocalPort, tunnel.Domains)
		case tunnel.RemotePort != 0:
			fmt.Printf("    %-16s %-6s %s:%d  ← port %d\n", tunnel.Name, tunnel.Type,
				tunnel.LocalIP, tunnel.LocalPort, tunnel.RemotePort)
		default:
			fmt.Printf("    %-16s %-6s %s:%d  ← server-allocated port\n", tunnel.Name,
				tunnel.Type, tunnel.LocalIP, tunnel.LocalPort)
		}
	}

	return nil
}
