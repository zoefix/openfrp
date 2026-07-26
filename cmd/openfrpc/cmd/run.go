package cmd

import (
	"context"
	"flag"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/tunnel/client"
	"github.com/zoefix/openfrp/internal/version"
	"github.com/zoefix/openfrp/pkg/log"
)

func init() {
	register(&Command{
		Name:    "run",
		Summary: "run the tunnel client daemon",
		Run:     runDaemon,
	})
}

func runDaemon(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := fs.String("c", "/var/etc/openfrp.json",
		"path to the configuration file (rendered from UCI on OpenWrt)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.LoadClient(*configPath)
	if err != nil {
		return err
	}

	logger, err := log.Setup(log.Options{
		Level:  cfg.Log.Level,
		Format: log.Format(cfg.Log.Format),
	})
	if err != nil {
		return err
	}

	c, err := client.New(cfg, logger, version.Short())
	if err != nil {
		return err
	}

	logger.Info("starting openfrpc",
		"version", version.String(),
		"server", cfg.ServerAddr,
		"tunnels", len(cfg.EnabledTunnels()))

	if err := c.Run(ctx); err != nil {
		return err
	}

	logger.Info("shutdown complete")
	return nil
}
