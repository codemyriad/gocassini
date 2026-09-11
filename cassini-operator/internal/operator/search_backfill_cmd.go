package operator

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// `cassini-operator backfill-search`: index meetings published before the
// search index existed (D-623).
//
// A hand-run admin command rather than startup work, for the same reason
// backfill-nc-files is: it reads every promoted bundle on the volume, and that
// is not something an operator restart should silently begin doing.
//
// It is safe to re-run and safe to interrupt. A meeting already indexed from
// the same delivered artifact is skipped — one PROPFIND when the delivery
// stamped a checksum, a re-download to learn the digest when it did not
// (uploads from before OC-Checksum) — and a meeting it could not index is
// recorded with a reason rather than left to look searched.
const backfillSearchCommand = "backfill-search"

// backfillSearchTimeout bounds the whole run. Like the NC backfill this is
// interactive with a human watching, so it is generous rather than tight.
const backfillSearchTimeout = 2 * time.Hour

const (
	// backfillSearchExitOK: the run completed. Meetings it could not index are
	// reported and recorded, and that is a normal outcome rather than a failure
	// — an archive always has some.
	backfillSearchExitOK = 0
	// backfillSearchExitUsage: bad flags.
	backfillSearchExitUsage = 2
	// backfillSearchExitNotStarted: nothing was read or written. Distinguished
	// because the index is disposable, so "nothing happened" and "the index is
	// now partly rebuilt" call for different next steps.
	backfillSearchExitNotStarted = 3
	// backfillSearchExitFailed: the run started and could not finish.
	backfillSearchExitFailed = 1
)

func runBackfillSearch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini-operator "+backfillSearchCommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false,
		"list what would be indexed, without opening or writing the index")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Index meetings that were published before the search index existed.

Reads the archive's catalog to learn which meetings exist and what each one's
recording is called, then indexes each from its promoted bundle on this
volume. A bundle is used only when its digest matches the checksum the
archive records for the delivered recording; otherwise the recording itself
is downloaded and indexed, so what search cites is always what a caller can
play. A read that fails is counted failed and changes nothing — re-running
is the fix. The job database is not touched.

Safe to re-run and safe to interrupt. Normal publishing indexes new meetings
on its own and needs nothing from this command.

Usage:
  cassini-operator `+backfillSearchCommand+` [--dry-run]

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
		// The archive catalog is the only source of the join key, and reading it
		// needs the AppAPI identity. Without it there is nothing to backfill
		// FROM, which is a different thing from an empty archive.
		fmt.Fprintf(stderr, "%s needs the AppAPI environment (NEXTCLOUD_URL, APP_SECRET, EX_APP_ID); nothing was read\n", backfillSearchCommand)
		return backfillSearchExitNotStarted
	}

	cfg, code, err := loadConfig(nil, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "operator config: %v\n", err)
		return backfillSearchExitNotStarted
	}
	if code != 0 {
		return backfillSearchExitNotStarted
	}

	// The archive root depends on the recorded storage mode (D-616), and this
	// is its own process: resolve the mode the way operator startup does. An
	// install with no recorded mode is refused rather than guessed at — the
	// unresolved fallback addresses the Team-folder root, and on a default-mode
	// install that would report a healthy archive as empty.
	ncStorage.setPath(storageSettingsPath(cfg))
	storage, err := LoadStorageSettings(ncStorage.settingsPath())
	if err != nil {
		fmt.Fprintf(stderr, "read storage settings: %v\nnothing was read or written\n", err)
		return backfillSearchExitNotStarted
	}
	if !storage.Configured() {
		fmt.Fprintf(stderr, "no storage mode is recorded for this install; enable the app so it can resolve one, or choose who can see recordings in Operator › Settings\nnothing was read or written\n")
		return backfillSearchExitNotStarted
	}
	storageSource := storage.Source
	if storageSource == "" {
		storageSource = storageModeSourceConfigured
	}
	ncStorage.set(storage.AccessControlled(), storageSource, storage.Clean())

	runCtx, cancel := context.WithTimeout(ctx, backfillSearchTimeout)
	defer cancel()

	logger := log.New(stderr, "backfill-search: ", log.LstdFlags)
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
		fmt.Fprintf(stdout, "would consider %d meeting(s):\n", len(targets))
		for _, target := range targets {
			fmt.Fprintf(stdout, "  %s  (job %s)\n", target.OpusName, target.JobID)
		}
		return backfillSearchExitOK
	}

	index, err := openSearchStore(searchStorePath(cfg.DBPath), logger)
	if err != nil {
		fmt.Fprintf(stderr, "open search index: %v\nnothing was written\n", err)
		return backfillSearchExitNotStarted
	}
	defer index.Close()

	rt := &Runtime{cfg: cfg, logger: logger, searchStore: index}
	// What was delivered is the archive's record to give — one PROPFIND per
	// meeting — and the archive reader is the fallback for meetings this
	// operator has no local copy of, which after a volume rebuild can be most
	// of them.
	delivered := exapp.archiveDeliveredState()
	archive := exapp.archiveOpusReader(cfg.CassiniBin, cfg.WorkRoot)
	report, err := rt.backfillSearchIndex(runCtx, targets, delivered, archive)
	if err != nil {
		fmt.Fprintf(stderr, "backfill failed: %v\n", err)
		return backfillSearchExitFailed
	}

	fmt.Fprintf(stdout, "indexed=%d unchanged=%d not-searchable=%d failed=%d of %d meeting(s)\n",
		report.Indexed, report.Unchanged, report.Unavailable, report.Failed, len(targets))
	if report.Unavailable > 0 || report.Failed > 0 {
		// Said out loud rather than left to be inferred from the counts: a
		// partially covered index is the normal state of a real archive, and an
		// operator should know it is expected rather than a broken run.
		fmt.Fprintf(stdout, "meetings that could not be indexed are recorded with a reason and reported as outside search coverage, not as having no matches\n")
	}
	return backfillSearchExitOK
}

// archiveBackfillTargets reads the authoritative catalog as the recordings
// owner and returns one target per meeting.
//
// The join key comes from each entry's audioPath, exactly as it does at
// publish: the per-caller visibility scan returns `.opus` basenames, and the
// catalog id, the job id and the packed filename coincide by convention only.
func (c ExAppConfig) archiveBackfillTargets(ctx context.Context) ([]searchBackfillTarget, error) {
	client := &http.Client{Timeout: ncFilesUploadTimeout}
	raw, status, err := c.davGetBytes(ctx, client, ncRecordingsOwner, ncArchiveRoot()+"/catalog.json")
	if err != nil {
		return nil, err
	}
	// Branch on STATUS, never on err alone: davGetBytes returns a nil error for
	// a 404, so reading the absent-archive case off err would turn an outage
	// into "nothing to do".
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("archive catalog -> HTTP %d", status)
	}
	return parseBackfillTargets(raw)
}

// parseBackfillTargets decodes the catalog into targets, skipping entries that
// cannot be keyed. A legacy directory-shaped entry has no basename any
// visibility scan can return, so indexing it would make it permanently
// unreachable.
func parseBackfillTargets(raw []byte) ([]searchBackfillTarget, error) {
	var catalog struct {
		Meetings []struct {
			ID        string `json:"id"`
			JobID     string `json:"jobId"`
			AudioPath string `json:"audioPath"`
		} `json:"meetings"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, fmt.Errorf("parse archive catalog: %w", err)
	}
	targets := make([]searchBackfillTarget, 0, len(catalog.Meetings))
	for _, entry := range catalog.Meetings {
		ref := strings.TrimSpace(entry.AudioPath)
		if ref == "" {
			continue
		}
		name := path.Base(filepath.ToSlash(ref))
		if name == "" || name == "." || name == "/" {
			continue
		}
		// jobId is carried explicitly since D-640, but a meeting published
		// before that field existed has none — and for the operator's own
		// publishes the catalog id IS the job id. Prefer the explicit field and
		// fall back, rather than assuming either.
		jobID := strings.TrimSpace(entry.JobID)
		if jobID == "" {
			jobID = strings.TrimSpace(entry.ID)
		}
		if jobID == "" {
			continue
		}
		targets = append(targets, searchBackfillTarget{JobID: jobID, OpusName: name})
	}
	return targets, nil
}
