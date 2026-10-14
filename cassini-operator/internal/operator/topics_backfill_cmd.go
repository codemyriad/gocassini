package operator

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
)

// `cassini-operator backfill-topics`: queue past meetings for topic
// suggestions, like "Check past meetings" in Settings. It only queues: the
// running operator's worker does the work, at backfill priority, so it is
// safe to run while the operator serves.
const backfillTopicsCommand = "backfill-topics"

func runBackfillTopics(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini-operator "+backfillTopicsCommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	since := fs.String("since", "", "only meetings recorded on or after this date (YYYY-MM-DD)")
	limit := fs.Int("limit", 0, "queue at most this many meetings (0: all)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Queue past meetings for topic suggestions.

Reads the owner's Nextcloud recording directory and queues every meeting that
topic suggestions have not seen yet. The running operator checks them one at a
time, after new recordings and builds. Safe to re-run: a queued or checked
meeting is not queued twice.

Usage:
  cassini-operator `+backfillTopicsCommand+` [--since YYYY-MM-DD] [--limit N]

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	from, err := parseTopicSince(*since)
	if err != nil || fs.NArg() != 0 || *limit < 0 {
		if err == nil {
			err = fmt.Errorf("unexpected arguments")
		}
		fmt.Fprintf(stderr, "%s: %v\n", backfillTopicsCommand, err)
		return 2
	}
	exapp, err := LoadExAppConfig()
	if err != nil {
		fmt.Fprintf(stderr, "exapp config: %v\n", err)
		return 3
	}
	if !exapp.appAPIActive() {
		fmt.Fprintf(stderr, "%s needs the AppAPI environment (NEXTCLOUD_URL, APP_SECRET, EX_APP_ID); nothing was queued\n", backfillTopicsCommand)
		return 3
	}
	cfg, code, err := loadConfig(nil, stderr)
	if err != nil || code != 0 {
		if err != nil {
			fmt.Fprintf(stderr, "operator config: %v\n", err)
		}
		return 3
	}
	jobs, err := OpenStore(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(stderr, "open job database: %v\n", err)
		return 3
	}
	defer jobs.Close()
	exapp.lifecycle = jobs
	annotations, err := openAnnotationStore(sidecarPath(cfg.DBPath, annotationsStoreFilename), nil)
	if err != nil {
		fmt.Fprintf(stderr, "open annotations database: %v\n", err)
		return 3
	}
	defer annotations.Close()
	store := newTopicsStore(annotations)
	store.lifecycle = jobs
	targets, err := exapp.archiveBackfillTargets(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "read owner recording inventory: %v\nnothing was queued\n", err)
		return 3
	}
	result, err := queueTopicBackfill(ctx, store, jobs, targets, from, *limit)
	if err != nil {
		fmt.Fprintf(stderr, "queue: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "queued=%d of %d meeting(s); about %d s of work is waiting\n", result.Queued, len(targets), result.EstimateSeconds)
	return 0
}
