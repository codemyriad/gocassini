package main

import (
	"log"
	"os"

	"gocassini/internal/app"
	"gocassini/internal/config"
)

func main() {
	cfg, err := config.FromFlags(os.Args[1:])
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	if err := app.Run(cfg); err != nil {
		log.Fatalf("run error: %v", err)
	}
}
