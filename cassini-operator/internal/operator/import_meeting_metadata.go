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
	"os"
	"path"
	"strings"
)

// import-meeting-metadata restores portable catalog descriptions against the
// destination inventory. Source-instance file IDs and permissions are never
// imported. Validate the entire mapping before opening the metadata database.
func runImportMeetingMetadata(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import-meeting-metadata", flag.ContinueOnError)
	fs.SetOutput(stderr)
	catalogFile := fs.String("catalog", "", "portable catalog.json to import")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *catalogFile == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "--catalog is required")
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(stderr, err); return 1 }
	raw, err := os.ReadFile(*catalogFile)
	if err != nil {
		return fail(err)
	}
	var catalog siteCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return fail(err)
	}
	exapp, err := LoadExAppConfig()
	if err != nil {
		return fail(err)
	}
	if !exapp.appAPIActive() {
		return fail(fmt.Errorf("import requires the AppAPI environment"))
	}
	names, err := exapp.ownerRecordingNames(ctx, &http.Client{Timeout: ncProvisionTimeout})
	if err != nil {
		return fail(err)
	}
	rows, err := mapSeedMetadata(catalog, names)
	if err != nil {
		return fail(err)
	}
	cfg, code, err := loadConfig(nil, stderr)
	if err != nil {
		return fail(err)
	}
	if code != 0 {
		return code
	}
	store, err := openMeetingMetadataStore(meetingMetadataPath(cfg.DBPath), log.New(stderr, "metadata: ", 0))
	if err != nil {
		return fail(err)
	}
	defer store.Close()
	for _, row := range rows {
		if err := store.Put(ctx, row.id, row.name, row.entry); err != nil {
			return fail(err)
		}
	}
	fmt.Fprintf(stdout, "imported metadata for %d recording(s) using destination file IDs\n", len(rows))
	return 0
}

type seedMetadataRow struct {
	id    int64
	name  string
	entry json.RawMessage
}

func mapSeedMetadata(catalog siteCatalog, inventory map[int64]string) ([]seedMetadataRow, error) {
	if catalog.Version != "cassini.viewer.catalog.v1" || len(catalog.Meetings) == 0 {
		return nil, fmt.Errorf("expected a non-empty cassini.viewer.catalog.v1 catalog")
	}
	ids := make(map[string]int64, len(inventory))
	for id, name := range inventory {
		ids[name] = id
	}
	seen, seenIDs := map[string]bool{}, map[string]bool{}
	rows := make([]seedMetadataRow, 0, len(catalog.Meetings))
	for _, entry := range catalog.Meetings {
		var probe struct {
			ID        string `json:"id"`
			AudioPath string `json:"audioPath"`
		}
		if err := json.Unmarshal(entry, &probe); err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(probe.AudioPath, "./meetings/")
		if probe.ID == "" || seenIDs[probe.ID] || probe.AudioPath != "./meetings/"+name || path.Base(name) != name || !strings.HasSuffix(name, ".opus") || seen[name] || ids[name] <= 0 {
			return nil, fmt.Errorf("invalid, duplicate, or absent destination recording: %q", probe.AudioPath)
		}
		seen[name], seenIDs[probe.ID] = true, true
		rows = append(rows, seedMetadataRow{ids[name], name, entry})
	}
	return rows, nil
}
