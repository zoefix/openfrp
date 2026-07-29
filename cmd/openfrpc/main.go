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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := cmd.Execute(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "openfrpc:", err)
		os.Exit(1)
	}
}
