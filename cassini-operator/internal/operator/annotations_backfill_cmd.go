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

// `cassini-operator backfill-annotations`: rebuild the marks projection from
// the recordings themselves (D-737).
//
// The plumbing is backfill-search's, for backfill-search's reasons: a hand-run
// admin command rather than startup work, because it can download the whole
// archive; the same catalog as the source of join keys; the same storage-mode
// resolution so it reads the root the running operator reads.
//
// It is what makes the projection disposable in practice. A deleted, corrupt or
// schema-bumped annotations.sqlite3 is refilled by running this, and on a fresh
// volume it adopts the namespace the archive's files carry, so the first mark
// written afterwards joins the existing tags rather than starting a second
// namespace.
const backfillAnnotationsCommand = "backfill-annotations"

// backfillAnnotationsTimeout bounds the whole run, generously: a human is
// watching, and a first rebuild downloads every recording.
const backfillAnnotationsTimeout = 2 * time.Hour

const (
	// backfillAnnotationsExitOK: the run completed. Meetings it could not read
	// are reported and recorded; an archive always has some.
	backfillAnnotationsExitOK = 0
	// backfillAnnotationsExitFailed: the run started and could not finish.
	backfillAnnotationsExitFailed = 1
	// backfillAnnotationsExitUsage: bad flags.
	backfillAnnotationsExitUsage = 2
	// backfillAnnotationsExitNotStarted: nothing was read or written.
	backfillAnnotationsExitNotStarted = 3
)

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
without a download. Only the delivered copies in Nextcloud are read — marks are
never on this volume's working copies. A read that fails is counted failed and
leaves what was indexed before; re-running is the fix.

On a fresh index it adopts the tag namespace most of the archive's recordings
carry, so marks written afterwards join the existing tags.

Safe to re-run and safe to interrupt. Marking a meeting and publishing one both
index it on their own and need nothing from this command.

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
		return backfillAnnotationsExitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		return backfillAnnotationsExitUsage
	}

	exapp, err := LoadExAppConfig()
	if err != nil {
		fmt.Fprintf(stderr, "exapp config: %v\n", err)
		return backfillAnnotationsExitNotStarted
	}
	if !exapp.appAPIActive() {
		fmt.Fprintf(stderr, "%s needs the AppAPI environment (NEXTCLOUD_URL, APP_SECRET, EX_APP_ID); nothing was read\n", backfillAnnotationsCommand)
		return backfillAnnotationsExitNotStarted
	}
	cfg, code, err := loadConfig(nil, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "operator config: %v\n", err)
		return backfillAnnotationsExitNotStarted
	}
	if code != 0 {
		return backfillAnnotationsExitNotStarted
	}
	if !resolveBackfillStorageMode(cfg, stderr) {
		return backfillAnnotationsExitNotStarted
	}

	runCtx, cancel := context.WithTimeout(ctx, backfillAnnotationsTimeout)
	defer cancel()

	logger := log.New(stderr, backfillAnnotationsCommand+": ", log.LstdFlags)
	targets, err := exapp.archiveBackfillTargets(runCtx)
	if err != nil {
		fmt.Fprintf(stderr, "read archive catalog: %v\nnothing was read or written\n", err)
		return backfillAnnotationsExitNotStarted
	}
	if len(targets) == 0 {
		fmt.Fprintf(stdout, "the archive names no meetings; nothing to index\n")
		return backfillAnnotationsExitOK
	}
	if *dryRun {
		fmt.Fprintf(stdout, "would read %d recording(s):\n", len(targets))
		for _, target := range targets {
			fmt.Fprintf(stdout, "  %s\n", target.OpusName)
		}
		return backfillAnnotationsExitOK
	}

	store, err := openAnnotationStore(annotationStorePath(cfg.DBPath), logger)
	if err != nil {
		fmt.Fprintf(stderr, "open annotations index: %v\nnothing was written\n", err)
		return backfillAnnotationsExitNotStarted
	}
	defer store.Close()

	report, err := backfillAnnotationIndex(runCtx, store, logger, targets,
		exapp.archiveDeliveredState(), exapp.archiveAnnotationReader(cfg.CassiniBin, cfg.WorkRoot))
	if err != nil {
		fmt.Fprintf(stderr, "backfill failed: %v\n", err)
		return backfillAnnotationsExitFailed
	}

	fmt.Fprintf(stdout, "indexed=%d unchanged=%d unreadable=%d failed=%d of %d recording(s)\n",
		report.Indexed, report.Unchanged, report.Unavailable, report.Failed, len(targets))
	switch ns := report.Namespace; {
	case ns.Adopted:
		fmt.Fprintf(stdout, "adopted the archive's tag namespace %s\n", ns.Namespace)
	case ns.Archive != "":
		// One installation is meant to have one namespace. Said out loud and
		// not "fixed": which one is right is a question for a person.
		fmt.Fprintf(stdout, "warning: this install's tag namespace is %s, but most recordings carry %s; the same label in the two will not be the same tag\n",
			ns.Namespace, ns.Archive)
	case ns.Namespace == "":
		fmt.Fprintf(stdout, "no recording carries a tag namespace yet; one will be minted with the first mark\n")
	}
	if report.Unavailable > 0 || report.Failed > 0 {
		fmt.Fprintf(stdout, "recordings whose marks could not be read are reported outside tag coverage, not as untagged\n")
	}
	return backfillAnnotationsExitOK
}
