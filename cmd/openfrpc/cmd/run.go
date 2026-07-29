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

	supervisor, err := client.NewSupervisor(cfg, logger, version.Short())
	if err != nil {
		return err
	}

	bound := boundCertificates(cfg)
	if service, err := manage.New(dbPath); err != nil {
		logger.Warn("the database cannot be opened, so certificate push and "+
			"traffic history are unavailable; tunnels are unaffected",
			"error", err)
	} else {
		defer service.Close()

		if bound > 0 {
			supervisor.SetCertSource(service.NewCertSource())
			logger.Info("certificate push enabled", "bound_tunnels", bound)
		}

		history := client.NewTrafficHistory(supervisor.Traffic(), service.Traffic(), logger)
		go history.Run(ctx)
	}

	logger.Info("starting openfrpc",
		"version", version.String(),
		"servers", len(cfg.Upstreams()),
		"tunnels", len(cfg.EnabledTunnels()))

	if err := supervisor.Run(ctx); err != nil {
		return err
	}

	logger.Info("shutdown complete")
	return nil
}

func boundCertificates(cfg *config.Client) int {
	count := 0
	for _, tunnel := range cfg.EnabledTunnels() {
		if tunnel.CertID != 0 {
			count++
		}
	}
	return count
}
