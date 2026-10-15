package operator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// The retention provenance gate accepts a done import record exactly as it
// accepts a job with a successful publish (D-828): the archive file is adopted
// and aged from the meeting's own dates. A file matching neither source stays
// skipped, unadopted.
func TestRetentionInventoryAdoptsDoneImports(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	ctx := context.Background()

	importName := "01IMPORTEDMEETING000000000.opus"
	job := ImportJob{
		ID: "01IMPORTEDMEETING000000000", BatchID: "b", CreatedBy: "alice",
		SourceName: "retro.opus", StagedPath: ".CassiniImports/x.opus",
	}
	if err := rt.store.CreateImportJobs(ctx, []ImportJob{job}); err != nil {
		t.Fatal(err)
	}

	metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), "meetings.sqlite3"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	entry, _ := json.Marshal(map[string]string{
		"audioPath": "./meetings/" + importName, "createdAtUtc": "2026-08-12T10:00:00Z",
	})
	if err := metadata.Put(ctx, 77, importName, entry); err != nil {
		t.Fatal(err)
	}
	service := &annotationService{rt: rt, exapp: ExAppConfig{meetingMetadata: metadata, sharePaths: &recordingSharePathCache{
		ownerNames:   map[int64]string{77: importName, 88: "01UNMANAGEDSTRAYFILE000000.opus"},
		ownerExpires: time.Now().Add(time.Hour),
	}}}

	// Before the import finishes, the archive file has no provenance: skipped.
	meetings, skipped, err := service.retentionInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(meetings) != 0 || len(skipped) != 2 {
		t.Fatalf("incomplete import adopted: %+v %+v", meetings, skipped)
	}

	if err := rt.store.SetImportJobOutcome(ctx, job.ID, importName, 77, "2026-10-15T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	meetings, skipped, err = service.retentionInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(meetings) != 1 || meetings[0].Name != importName {
		t.Fatalf("done import not adopted: %+v", meetings)
	}
	if meetings[0].Anchor != "2026-08-12T10:00:00Z" || meetings[0].AnchorSource != "createdAtUtc" {
		t.Fatalf("import aged from the wrong anchor: %+v", meetings[0])
	}
	// The stray file with neither a job nor an import record stays unmanaged.
	if len(skipped) != 1 || skipped[0].Name != "01UNMANAGEDSTRAYFILE000000.opus" {
		t.Fatalf("stray adoption: %+v", skipped)
	}
}
