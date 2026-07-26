// Command openfrpc is the OpenFrp client.
//
// It runs on the router and carries three responsibilities: maintaining the
// tunnels, provisioning the server over SSH, and — from P5 onward — managing
// DNS records and TLS certificates locally.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/zoefix/openfrp/cmd/openfrpc/cmd"
)

func main() {
	// SIGTERM is what procd sends on OpenWrt; SIGINT is the interactive case.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := cmd.Execute(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "openfrpc:", err)
		os.Exit(1)
	}
}
