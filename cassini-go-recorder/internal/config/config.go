package config

import (
	"errors"
	"flag"
	"fmt"
	"time"
)

type Config struct {
	Mode                    string
	OutputPath              string
	FinalOutputPath         string
	SegmentsDir             string
	CleanupIntermediate     bool
	SimTracks               int
	SimPackets              int
	Duration                time.Duration
	CallURL                 string
	GuestName               string
	JoinFlags               int
	Insecure                bool
	DebugSignaling          bool
	RequestOfferInterval    time.Duration
	MaxRequestOfferAttempts int
	TurnMode                string
}

func FromFlags(args []string) (Config, error) {
	fs := flag.NewFlagSet("gocassini", flag.ContinueOnError)

	var cfg Config
	var durationSeconds int
	var requestOfferIntervalSeconds float64
	fs.StringVar(&cfg.Mode, "mode", "simulate", "simulate or talk")
	fs.StringVar(&cfg.OutputPath, "output", "/tmp/gocassini.csr", "path to packet archive output file (.csr)")
	fs.StringVar(&cfg.FinalOutputPath, "final-output", "", "path to composed multi-track MKV output (default: derived from --output)")
	fs.StringVar(&cfg.SegmentsDir, "segments-dir", "", "directory for intermediate per-session files (default: unique mktemp dir next to --final-output)")
	fs.BoolVar(&cfg.CleanupIntermediate, "cleanup-intermediate", false, "remove intermediate segment files after successful compose")
	fs.IntVar(&cfg.SimTracks, "sim-tracks", 3, "number of synthetic tracks in simulate mode")
	fs.IntVar(&cfg.SimPackets, "sim-packets", 150, "packets per synthetic track in simulate mode")
	fs.IntVar(&durationSeconds, "duration", 30, "runtime duration in seconds")
	fs.StringVar(&cfg.CallURL, "call-url", "", "Nextcloud Talk call URL (required in talk mode)")
	fs.StringVar(&cfg.GuestName, "name", "GocassiniObserver", "display name for the observer guest")
	fs.IntVar(&cfg.JoinFlags, "join-flags", 1, "call join flags (must include bit 1)")
	fs.BoolVar(&cfg.Insecure, "insecure", false, "disable TLS certificate verification (testing only)")
	fs.BoolVar(&cfg.DebugSignaling, "debug-signaling", false, "log signaling events and peer messages")
	fs.Float64Var(&requestOfferIntervalSeconds, "request-offer-interval", 2.0, "seconds between requestoffer retries (0 disables)")
	fs.IntVar(&cfg.MaxRequestOfferAttempts, "max-request-offer-attempts", 8, "max requestoffer attempts per session (0 disables limit)")
	fs.StringVar(&cfg.TurnMode, "turn-mode", "all", "TURN usage mode: off, udp-only, all")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	cfg.Duration = time.Duration(durationSeconds) * time.Second
	cfg.RequestOfferInterval = time.Duration(requestOfferIntervalSeconds * float64(time.Second))

	if cfg.Mode != "simulate" && cfg.Mode != "talk" {
		return Config{}, fmt.Errorf("unsupported mode %q", cfg.Mode)
	}
	if cfg.Mode == "talk" && cfg.CallURL == "" {
		return Config{}, errors.New("call-url must be provided in talk mode")
	}
	if cfg.OutputPath == "" {
		return Config{}, errors.New("output path must not be empty")
	}
	if cfg.SimTracks < 1 {
		return Config{}, errors.New("sim-tracks must be >= 1")
	}
	if cfg.SimPackets < 1 {
		return Config{}, errors.New("sim-packets must be >= 1")
	}
	if cfg.JoinFlags <= 0 {
		return Config{}, errors.New("join-flags must be > 0")
	}
	if (cfg.JoinFlags & 1) == 0 {
		return Config{}, errors.New("join-flags must include bit 1 (in-call)")
	}
	if cfg.RequestOfferInterval < 0 {
		return Config{}, errors.New("request-offer-interval must be >= 0")
	}
	if cfg.MaxRequestOfferAttempts < 0 {
		return Config{}, errors.New("max-request-offer-attempts must be >= 0")
	}
	switch cfg.TurnMode {
	case "off", "udp-only", "all":
	default:
		return Config{}, fmt.Errorf("invalid turn-mode %q", cfg.TurnMode)
	}

	return cfg, nil
}
