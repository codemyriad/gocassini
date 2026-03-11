package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"gocassini/internal/cassini"
)

func main() {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	os.Exit(cassini.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
