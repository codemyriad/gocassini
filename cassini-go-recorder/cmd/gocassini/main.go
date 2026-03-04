package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"gocassini/internal/app"
	"gocassini/internal/config"
)

func main() {
	cfg, err := config.FromFlags(os.Args[1:])
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	if err := app.RunContext(ctx, cfg); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		log.Fatalf("run error: %v", err)
	}
}
