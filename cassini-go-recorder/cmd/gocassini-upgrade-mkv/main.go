package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gocassini/pkg/core/remux"
)

type config struct {
	InputPath     string
	ReportPath    string
	OutputPath    string
	WorkDir       string
	KeepWork      bool
	TitleOverride string
}

func main() {
	cfg, err := parseFlags()
	if err != nil {
		exitErr(err)
	}

	if err := remux.UpgradeLegacyMeetingMKV(cfg.InputPath, cfg.ReportPath, cfg.OutputPath, remux.UpgradeOptions{
		WorkDir:  cfg.WorkDir,
		KeepWork: cfg.KeepWork,
		Title:    cfg.TitleOverride,
	}); err != nil {
		exitErr(err)
	}

	fmt.Printf("input=%s\nreport=%s\noutput=%s\n", cfg.InputPath, cfg.ReportPath, cfg.OutputPath)
}

func parseFlags() (config, error) {
	var cfg config
	flag.StringVar(&cfg.InputPath, "input", "", "path to legacy meeting .mkv")
	flag.StringVar(&cfg.ReportPath, "report", "", "path to legacy recorder report (default: <input>.json)")
	flag.StringVar(&cfg.OutputPath, "output", "", "output compliant MKV path (default: <input>.v1.mkv)")
	flag.StringVar(&cfg.WorkDir, "work-dir", "", "temporary work directory (default: mktemp)")
	flag.BoolVar(&cfg.KeepWork, "keep-work", false, "keep temporary work directory after completion")
	flag.StringVar(&cfg.TitleOverride, "title", "", "optional container title metadata")
	flag.Parse()

	if strings.TrimSpace(cfg.InputPath) == "" {
		return config{}, errors.New("missing --input")
	}
	if strings.TrimSpace(cfg.ReportPath) == "" {
		cfg.ReportPath = cfg.InputPath + ".json"
	}
	if strings.TrimSpace(cfg.OutputPath) == "" {
		cfg.OutputPath = deriveDefaultOutputPath(cfg.InputPath)
	}
	return cfg, nil
}

func deriveDefaultOutputPath(inputPath string) string {
	ext := filepath.Ext(inputPath)
	if strings.EqualFold(ext, ".mkv") {
		return strings.TrimSuffix(inputPath, ext) + ".v1.mkv"
	}
	return inputPath + ".v1.mkv"
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
