package operator

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/oklog/ulid/v2"
)

// `cassini-operator import-meeting`: ingest portable meeting files from local
// paths (D-828). The hand-run twin of the viewer's Import tab, and the
// replacement for the manual runbook in docs/direct-shares-cutover.md — it is
// also the recovery path for meetings restored from a backup.
const importMeetingCommand = "import-meeting"

func runImportMeeting(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini-operator "+importMeetingCommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	asUser := fs.String("as", "", "local Nextcloud user the meetings are imported for (the share starter)")
	shareWith := fs.String("share-with", "", "comma-separated extra recipients: user:<id>, group:<id>, circle:<id>")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Import portable meeting files into this Cassini's archive.

Each file is validated with the cassini CLI, delivered byte for byte into the
private recordings archive under a freshly minted local id, indexed, adopted
into retention with the meeting's own dates as its age, and shared — with the
--as user always first, plus any --share-with recipients, all with resharing
allowed where the instance permits it. A done import is recorded durably in
the operator database; that record is what lets retention manage the file.

Both portable formats import: .opus recordings and transcription-only .json
documents. The format is detected from the file's content, not its name.

Usage:
  cassini-operator `+importMeetingCommand+` --as <user> [--share-with user:a,group:b] <file> [<file>...]

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
	if strings.TrimSpace(*asUser) == "" || fs.NArg() == 0 {
		fmt.Fprintln(stderr, "--as <user> and at least one file are required")
		return 2
	}
	audience, err := parseSharePrincipals(*shareWith)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(stderr, err); return 1 }

	exapp, err := LoadExAppConfig()
	if err != nil {
		return fail(err)
	}
	if !exapp.appAPIActive() {
		return fail(fmt.Errorf("%s needs the AppAPI environment (NEXTCLOUD_URL, APP_SECRET, EX_APP_ID)", importMeetingCommand))
	}
	cfg, code, err := loadConfig(nil, stderr)
	if err != nil {
		return fail(err)
	}
	if code != 0 {
		return code
	}
	unlock, err := lockWorkRoot(cfg.WorkRoot)
	if err != nil {
		return fail(fmt.Errorf("local archive is in use; stop the operator before importing: %w", err))
	}
	defer unlock()
	logger := log.New(stderr, importMeetingCommand+": ", log.LstdFlags)

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		return fail(err)
	}
	defer store.Close()
	exapp.lifecycle = store
	rt := &Runtime{cfg: cfg, store: store, logger: logger}
	if metadata, err := openMeetingMetadataStore(meetingMetadataPath(cfg.DBPath), logger); err != nil {
		logger.Printf("meeting metadata index unavailable (%v); imported meetings will list by filename until it is rebuilt", err)
	} else {
		defer metadata.Close()
		metadata.lifecycle = store
		rt.meetingMetadata = metadata
		exapp.meetingMetadata = metadata
	}
	if search, err := openSearchStore(searchStorePath(cfg.DBPath), logger); err != nil {
		logger.Printf("search index unavailable (%v); imported meetings will not be searchable until backfill", err)
	} else {
		defer search.Close()
		search.lifecycle = store
		rt.searchStore = search
	}
	if annotations, err := openAnnotationStore(sidecarPath(cfg.DBPath, annotationsStoreFilename), logger); err != nil {
		logger.Printf("annotations index unavailable (%v); imported marks will need backfill-annotations", err)
	} else {
		defer annotations.Close()
		rt.annotations = annotations
	}

	importer := newMeetingImporter(cfg, exapp, rt, logger)
	batchID := ulid.Make().String()
	failures := 0
	for _, file := range fs.Args() {
		local, err := filepath.Abs(file)
		if err == nil {
			_, err = os.Stat(local)
		}
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", file, err)
			failures++
			continue
		}
		job := ImportJob{
			ID: ulid.Make().String(), BatchID: batchID, CreatedBy: strings.TrimSpace(*asUser),
			SourceName: filepath.Base(file), StagedPath: "local:" + local,
			Sharing: importAudience{Share: len(audience) > 0, Audience: toImportPrincipals(audience)},
		}
		if err := store.CreateImportJobs(ctx, []ImportJob{job}); err != nil {
			return fail(err)
		}
		result, err := importer.run(ctx, meetingImportRequest{
			ImportID: job.ID, CreatedBy: job.CreatedBy, LocalPath: local, Audience: audience,
		}, func(state, phase string) {
			_ = store.SetImportJobState(ctx, job.ID, state, phase, "")
		})
		if err != nil {
			_ = store.SetImportJobState(ctx, job.ID, importStateFailed, "", err.Error())
			fmt.Fprintf(stderr, "%s: %v\n", file, err)
			failures++
			continue
		}
		_ = store.SetImportJobProvenance(ctx, job.ID, result.Format, result.ContentID, result.SourceRoomID, result.CreatedAtUTC, result.RecordedAtLocal)
		if err := store.SetImportJobOutcome(ctx, job.ID, result.DeliveredName, result.FileID, result.ImportedAtUTC); err != nil {
			return fail(err)
		}
		line, _ := json.Marshal(result)
		fmt.Fprintf(stdout, "%s -> %s\n", filepath.Base(file), string(line))
	}
	if failures > 0 {
		fmt.Fprintf(stderr, "%d of %d file(s) failed; the rest were imported\n", failures, fs.NArg())
		return 1
	}
	return 0
}

// parseSharePrincipals reads "user:alice,group:staff,circle:x" into principals
// the share reconciler accepts.
func parseSharePrincipals(raw string) ([]sharePrincipal, error) {
	var audience []sharePrincipal
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kind, id, ok := strings.Cut(part, ":")
		kind, id = strings.TrimSpace(kind), strings.TrimSpace(id)
		if _, known := shareTypeForPrincipal(kind); !ok || !known || id == "" {
			return nil, fmt.Errorf("invalid recipient %q: use user:<id>, group:<id> or circle:<id>", part)
		}
		audience = append(audience, sharePrincipal{Type: kind, ID: id})
	}
	return audience, nil
}

func toImportPrincipals(audience []sharePrincipal) []importPrincipal {
	out := make([]importPrincipal, 0, len(audience))
	for _, p := range audience {
		out = append(out, importPrincipal{Type: p.Type, ID: p.ID})
	}
	return out
}
