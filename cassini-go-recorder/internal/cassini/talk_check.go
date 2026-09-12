package cassini

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"time"

	"gocassini/internal/config"
	"gocassini/internal/talk"
)

func runTalkCheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("talk-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := config.Config{TalkAuthMode: config.TalkAuthModeHPBInternal}
	fs.StringVar(&cfg.CallURL, "call", "", "dedicated Talk test-room URL; no media is captured")
	fs.StringVar(&cfg.ConnectBaseURL, "connect-url", "", "configured Nextcloud connection URL")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := json.NewEncoder(stdout).Encode(talk.ProbeConnection(ctx, cfg)); err != nil {
		return 1
	}
	return 0
}
