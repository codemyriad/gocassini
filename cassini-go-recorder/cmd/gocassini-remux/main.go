package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"gocassini/pkg/core/remux"
)

type config struct {
	SessionPath   string
	OutputPath    string
	WorkDir       string
	KeepWork      bool
	StrictCodecs  bool
	PrintSummary  bool
	TitleOverride string
}

func main() {
	cfg, err := parseFlags()
	if err != nil {
		exitErr(err)
	}

	result, err := remux.BuildFromSession(cfg.SessionPath, cfg.OutputPath, remux.BuildOptions{
		WorkDir:      cfg.WorkDir,
		KeepWork:     cfg.KeepWork,
		StrictCodecs: cfg.StrictCodecs,
		Title:        cfg.TitleOverride,
	})
	if err != nil {
		exitErr(err)
	}

	if cfg.PrintSummary {
		fmt.Printf(
			"session=%s output=%s work_dir=%s segments=%d\n",
			result.SessionJSONPath,
			result.OutputPath,
			result.WorkDir,
			result.Segments,
		)
		for _, plan := range result.StreamPlans {
			fmt.Printf(
				"  stream=%s kind=%s codec=%s adjust_ns=%d offset_s=%.6f timeline_samples=%d\n",
				plan.StreamID,
				plan.Kind,
				plan.Codec,
				plan.TimelineAdjustNS,
				plan.OffsetSeconds,
				plan.TimelineSamples,
			)
		}
	}
}

func parseFlags() (config, error) {
	var cfg config
	flag.StringVar(&cfg.SessionPath, "session", "", "path to session.json or session directory")
	flag.StringVar(&cfg.OutputPath, "output", "", "final output MKV path")
	flag.StringVar(&cfg.WorkDir, "work-dir", "", "temporary work directory (default: mktemp)")
	flag.BoolVar(&cfg.KeepWork, "keep-work", false, "keep temporary work directory after completion")
	flag.BoolVar(&cfg.StrictCodecs, "strict-codecs", false, "fail if unsupported codecs are present (default: skip unsupported streams)")
	flag.BoolVar(&cfg.PrintSummary, "summary", true, "print final remux summary")
	flag.StringVar(&cfg.TitleOverride, "title", "", "optional container title metadata")
	flag.Parse()

	if strings.TrimSpace(cfg.SessionPath) == "" {
		return config{}, errors.New("missing --session")
	}
	if strings.TrimSpace(cfg.OutputPath) == "" {
		return config{}, errors.New("missing --output")
	}
	return cfg, nil
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
