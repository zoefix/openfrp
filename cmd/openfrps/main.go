// Command openfrps is the OpenFrp server daemon.
//
// It runs on a public host and does one job: move tunnel traffic. Certificate
// issuance and DNS management live on the client side, so nothing here needs
// cloud credentials.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/tunnel/server"
	"github.com/zoefix/openfrp/internal/version"
	"github.com/zoefix/openfrp/pkg/log"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "openfrps:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("c", "/etc/openfrp/openfrps.json", "path to the configuration file")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("openfrps", version.String())
		return nil
	}

	cfg, err := config.LoadServer(*configPath)
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

	srv, err := server.New(cfg, logger, version.Short())
	if err != nil {
		return err
	}

	// SIGTERM is what procd and systemd send; SIGINT is the interactive case.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("starting openfrps", "version", version.String())

	if err := srv.Serve(ctx); err != nil {
		return err
	}

	logger.Info("shutdown complete")
	return nil
}
