package operator

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"time"
)

// `cassini-operator backfill-annotations`: rebuild the tag index from the
// recordings themselves (D-737), with backfill-search's plumbing and exit
// codes. The operator runs it on its own when the index has never been built;
// this is for re-running it by hand.
const backfillAnnotationsCommand = "backfill-annotations"

// backfillAnnotationsTimeout bounds a whole run: a first rebuild downloads
// every recording.
const backfillAnnotationsTimeout = 2 * time.Hour

func runBackfillAnnotations(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini-operator "+backfillAnnotationsCommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false,
		"list the recordings that would be read, without opening or writing the index")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Rebuild the tag index from the marks inside each published recording.

Reads the archive's catalog to learn which recordings exist, then reads each
delivered recording's marks with `+"`cassini annotate show`"+` and records them. A
recording whose checksum matches the one it was last indexed from is skipped
without a download. A read that fails is counted failed and leaves what was
indexed before; re-running is the fix. Safe to re-run and safe to interrupt.

Usage:
  cassini-operator `+backfillAnnotationsCommand+` [--dry-run]

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return backfillSearchExitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		return backfillSearchExitUsage
	}

	exapp, err := LoadExAppConfig()
	if err != nil {
		fmt.Fprintf(stderr, "exapp config: %v\n", err)
		return backfillSearchExitNotStarted
	}
	if !exapp.appAPIActive() {
		fmt.Fprintf(stderr, "%s needs the AppAPI environment (NEXTCLOUD_URL, APP_SECRET, EX_APP_ID); nothing was read\n", backfillAnnotationsCommand)
		return backfillSearchExitNotStarted
	}
	cfg, code, err := loadConfig(nil, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "operator config: %v\n", err)
		return backfillSearchExitNotStarted
	}
	if code != 0 || !resolveBackfillStorageMode(cfg, stderr) {
		return backfillSearchExitNotStarted
	}

	runCtx, cancel := context.WithTimeout(ctx, backfillAnnotationsTimeout)
	defer cancel()

	logger := log.New(stderr, backfillAnnotationsCommand+": ", log.LstdFlags)
	targets, err := exapp.archiveBackfillTargets(runCtx)
	if err != nil {
		fmt.Fprintf(stderr, "read archive catalog: %v\nnothing was read or written\n", err)
		return backfillSearchExitNotStarted
	}
	if len(targets) == 0 {
		fmt.Fprintf(stdout, "the archive names no meetings; nothing to index\n")
		return backfillSearchExitOK
	}
	if *dryRun {
		fmt.Fprintf(stdout, "would read %d recording(s):\n", len(targets))
		for _, target := range targets {
			fmt.Fprintf(stdout, "  %s\n", target.OpusName)
		}
		return backfillSearchExitOK
	}

	store, err := openAnnotationStore(sidecarPath(cfg.DBPath, annotationsStoreFilename), logger)
	if err != nil {
		fmt.Fprintf(stderr, "open annotations index: %v\nnothing was written\n", err)
		return backfillSearchExitNotStarted
	}
	defer store.Close()

	report, err := backfillAnnotationIndex(runCtx, store, logger, targets,
		exapp.archiveDeliveredState(), exapp.archiveAnnotationReader(cfg.CassiniBin, cfg.WorkRoot))
	if err != nil {
		fmt.Fprintf(stderr, "backfill failed: %v\n", err)
		return backfillSearchExitFailed
	}
	fmt.Fprintf(stdout, "indexed=%d unchanged=%d unreadable=%d failed=%d of %d recording(s)\n",
		report.Indexed, report.Unchanged, report.Unavailable, report.Failed, len(targets))
	if report.Unavailable > 0 || report.Failed > 0 {
		fmt.Fprintf(stdout, "recordings whose marks could not be read are reported outside tag coverage, not as untagged\n")
	}
	return backfillSearchExitOK
}
