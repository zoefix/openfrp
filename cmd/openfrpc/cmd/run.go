package cmd

import (
	"context"
	"flag"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/manage"
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

	// Certificates for tunnels that terminate TLS come out of the local
	// database. Its absence is not fatal: on an architecture with no SQLite
	// driver, or before anything has been issued, the tunnels still run and
	// only edge termination is unavailable.
	if bound := boundCertificates(cfg); bound > 0 {
		service, err := manage.New(dbPath)
		if err != nil {
			logger.Warn("tunnels are bound to certificates but the database "+
				"cannot be opened, so none will be pushed",
				"bound", bound, "error", err)
		} else {
			defer service.Close()
			c.SetCertSource(service.NewCertSource())
			logger.Info("certificate push enabled", "bound_tunnels", bound)
		}
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

// boundCertificates counts the enabled tunnels that name a certificate.
//
// Opening the database is skipped entirely when none do, so a plain tunnelling
// setup neither touches SQLite nor warns about it.
func boundCertificates(cfg *config.Client) int {
	count := 0
	for _, tunnel := range cfg.EnabledTunnels() {
		if tunnel.CertID != 0 {
			count++
		}
	}
	return count
}
