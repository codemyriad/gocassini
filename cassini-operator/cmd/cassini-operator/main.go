package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"cassini-operator/internal/operator"
)

func main() {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	os.Exit(operator.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
