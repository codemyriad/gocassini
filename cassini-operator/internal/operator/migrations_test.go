package operator

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrationsRunContiguouslyThroughSpeakerEdits: loadMigrations refuses a
// gap or a reused version, and the speaker edits come after the media
// cleanup, whatever numbers the two end up with.
func TestMigrationsRunContiguouslyThroughSpeakerEdits(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	at := migrationIndex(migrations, "speaker_edits")
	if at < 1 || migrations[at-1].Name != "media_cleanup" {
		t.Fatalf("speaker_edits is at %d, want it right after media_cleanup", at)
	}
}

// migrationIndex finds a migration by name, so a test about one migration does
// not break each time another is added after it. -1 when there is none.
func migrationIndex(migrations []migration, name string) int {
	for i, m := range migrations {
		if m.Name == name {
			return i
		}
	}
	return -1
}

// TestMigrateDownRemovesSpeakerEditsAndUpRestoresThem: a rollback to the last
// release must leave job_attempts as that release reads it, and the speaker
// edits must come back on re-upgrade.
func TestMigrateDownRemovesSpeakerEditsAndUpRestoresThem(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	at := migrationIndex(migrations, "speaker_edits")
	if at < 0 {
		t.Fatal("no speaker_edits migration")
	}
	speakerEdits := migrations[at]
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	seedJobRow(t, store.db, seededJobRow{ID: "kept", Stage: "done", State: "succeeded", CreatedAt: "2026-10-01T10:00:00Z"})

	if err := store.migrateDownTo(speakerEdits.Version - 1); err != nil {
		t.Fatalf("migrateDownTo(%d) error = %v", speakerEdits.Version-1, err)
	}
	for _, table := range []string{"speaker_edits", "speaker_split_turns"} {
		if sqliteTableExists(t, store.db, table) {
			t.Fatalf("%s survived the down migration", table)
		}
	}
	if _, err := store.db.Exec(`SELECT speaker_edits_json FROM job_attempts`); err == nil {
		t.Fatal("job_attempts.speaker_edits_json survived the down migration")
	}
	if _, err := store.GetJob(context.Background(), "kept"); err != nil {
		t.Fatalf("the down migration lost a job: %v", err)
	}

	if err := store.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema() error = %v", err)
	}
	if _, err := store.GetSpeakerEdits(context.Background(), "kept"); err != nil {
		t.Fatalf("speaker edits did not come back on re-upgrade: %v", err)
	}
	if _, _, err := store.AttemptSpeakerEdits(context.Background(), "kept", 1); err != nil {
		t.Fatalf("attempt speaker edits did not come back on re-upgrade: %v", err)
	}
}

// TestMigrateDownRemovesTheInsightTablesAndUpRestoresThem exercises 0009 the way
// an installation upgrading from the last release does, and the way a rollback
// does: the down migration must leave the job tables — which are the operator's
// only other state — exactly where it found them.
func TestMigrateDownRemovesTheInsightTablesAndUpRestoresThem(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	for _, table := range []string{"insight_runs", "insight_run_attempts"} {
		if !sqliteTableExists(t, store.db, table) {
			t.Fatalf("%s is missing after ensureSchema()", table)
		}
	}

	if err := store.migrateDownTo(7); err != nil {
		t.Fatalf("migrateDownTo(7) error = %v", err)
	}
	for _, table := range []string{"insight_runs", "insight_run_attempts"} {
		if sqliteTableExists(t, store.db, table) {
			t.Fatalf("%s survived the down migration", table)
		}
	}
	for _, table := range []string{"jobs", "job_attempts"} {
		if !sqliteTableExists(t, store.db, table) {
			t.Fatalf("the insight down migration removed %s", table)
		}
	}
	if versions := migrationVersions(t, store.db); len(versions) != 7 || versions[len(versions)-1] != 7 {
		t.Fatalf("applied versions after down = %v, want 1..7", versions)
	}

	if err := store.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema() error = %v", err)
	}
	for _, table := range []string{"insight_runs", "insight_run_attempts"} {
		if !sqliteTableExists(t, store.db, table) {
			t.Fatalf("%s did not come back on re-upgrade", table)
		}
	}
}

// TestInsightRunListUsesItsIndex keeps the list query off a full scan and, more
// to the point, off a sort: the browse surface asks for one caller's runs newest
// first on every mount.
func TestInsightRunListUsesItsIndex(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	rows, err := store.db.Query(`
EXPLAIN QUERY PLAN`+insightRunSelect+`
WHERE created_by = ?
ORDER BY created_at DESC, id DESC`, "alice")
	if err != nil {
		t.Fatalf("explain insight run list: %v", err)
	}
	defer rows.Close()

	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	planText := strings.Join(plan, "\n")
	if !strings.Contains(planText, "insight_runs_created_by_created_desc") || strings.Contains(planText, "TEMP B-TREE") {
		t.Fatalf("insight run list does not read its index in order:\n%s", planText)
	}
}

// A rollback past 0017 drops the name and keeps the run; the re-upgrade brings
// the column back empty, which reads as "no name".
func TestMigrateDownRemovesTheInsightNameAndKeepsTheRun(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	at := migrationIndex(migrations, "insight_name")
	if at < 0 {
		t.Fatal("no insight_name migration")
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	runs := newInsightStore(store)
	ctx := context.Background()
	if err := runs.CreateRun(ctx, InsightRun{
		ID: "ins_0123456789abcdef", CreatedBy: "alice", WorkflowID: "summarise",
		MeetingIDs: []string{"mtg_1111111111111111"}, Name: "Design sync",
	}); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	if err := store.migrateDownTo(migrations[at].Version - 1); err != nil {
		t.Fatalf("migrateDownTo(%d) error = %v", migrations[at].Version-1, err)
	}
	if _, err := store.db.Exec(`SELECT name FROM insight_runs`); err == nil {
		t.Fatal("insight_runs.name survived the down migration")
	}
	if err := store.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema() error = %v", err)
	}
	run, err := runs.GetRun(ctx, "ins_0123456789abcdef")
	if err != nil || run.Name != "" {
		t.Fatalf("GetRun() after re-upgrade = %+v, %v; want the run back with no name", run, err)
	}
}

// A rollback past 0018 drops the document's file id and keeps the run; the
// re-upgrade brings the column back empty, which opens the folder rather than
// the file.
func TestMigrateDownRemovesTheInsightDocumentFileIDAndKeepsTheRun(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	at := migrationIndex(migrations, "insight_document_file_id")
	if at < 0 {
		t.Fatal("no insight_document_file_id migration")
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	runs := newInsightStore(store)
	ctx := context.Background()
	if err := runs.CreateRun(ctx, InsightRun{
		ID: "ins_0123456789abcdef", CreatedBy: "alice", WorkflowID: "summarise",
		MeetingIDs: []string{"mtg_1111111111111111"}, Name: "Design sync",
	}); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if _, err := runs.BeginAttempt(ctx, "ins_0123456789abcdef"); err != nil {
		t.Fatalf("BeginAttempt() error = %v", err)
	}
	if err := runs.FinishAttempt(ctx, "ins_0123456789abcdef", InsightOutcome{
		Status: insightStatusSucceeded, DocumentPath: ncInsightsRoot + "/Design sync.md", DocumentFileID: "4242",
	}); err != nil {
		t.Fatalf("FinishAttempt() error = %v", err)
	}
	if run, err := runs.GetRun(ctx, "ins_0123456789abcdef"); err != nil || run.DocumentFileID != "4242" {
		t.Fatalf("GetRun() = %+v, %v; want the file id kept", run, err)
	}

	if err := store.migrateDownTo(migrations[at].Version - 1); err != nil {
		t.Fatalf("migrateDownTo(%d) error = %v", migrations[at].Version-1, err)
	}
	if _, err := store.db.Exec(`SELECT document_file_id FROM insight_runs`); err == nil {
		t.Fatal("insight_runs.document_file_id survived the down migration")
	}
	if err := store.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema() error = %v", err)
	}
	run, err := runs.GetRun(ctx, "ins_0123456789abcdef")
	if err != nil || run.DocumentFileID != "" || run.DocumentPath != ncInsightsRoot+"/Design sync.md" {
		t.Fatalf("GetRun() after re-upgrade = %+v, %v; want the run back with its path and no file id", run, err)
	}
}

// A rollback past 0019 drops the import records entirely — with them goes the
// retention provenance for imported meetings, which the gate then simply stops
// adopting (a keep, never a delete). The re-upgrade brings the table back
// empty; done imports must then be re-recorded by re-importing, so this test
// pins that the rows really are gone rather than mangled.
func TestMigrateDownDropsImportJobs(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	at := migrationIndex(migrations, "import_jobs")
	if at < 0 {
		t.Fatal("no import_jobs migration")
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	job := ImportJob{ID: "01MIGRATIONTEST00000000000", BatchID: "b", CreatedBy: "alice", SourceName: "a.opus", StagedPath: "p"}
	if err := store.CreateImportJobs(ctx, []ImportJob{job}); err != nil {
		t.Fatalf("CreateImportJobs() error = %v", err)
	}
	if err := store.SetImportJobOutcome(ctx, job.ID, job.ID+".opus", 7, "2026-10-15T10:00:00Z"); err != nil {
		t.Fatalf("SetImportJobOutcome() error = %v", err)
	}

	if err := store.migrateDownTo(migrations[at].Version - 1); err != nil {
		t.Fatalf("migrateDownTo(%d) error = %v", migrations[at].Version-1, err)
	}
	if _, err := store.db.Exec(`SELECT id FROM import_jobs`); err == nil {
		t.Fatal("import_jobs survived the down migration")
	}
	if err := store.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema() error = %v", err)
	}
	if names, err := store.doneImportDeliveries(ctx); err != nil || len(names) != 0 {
		t.Fatalf("doneImportDeliveries() after re-upgrade = %v, %v; want an empty, working table", names, err)
	}
}

// TestNewestMigrationHasARollbackTest is the tripwire: a rollback to the last
// release is only known to work for a migration that has a down-and-up test in
// this file. Adding a migration means adding its test and naming it here, or
// this fails.
func TestNewestMigrationHasARollbackTest(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	covered := map[string]bool{
		"speaker_edits":            true,
		"insight_name":             true,
		"insight_document_file_id": true,
		"import_jobs":              true,
		"import_staging":           true,
	}
	newest := migrations[len(migrations)-1].Name
	if !covered[newest] {
		t.Fatalf("the newest migration %q has no rollback test in migrations_test.go; add one and list it here", newest)
	}
}

// Rollback preserves imported-meeting provenance and never lets the old worker
// automatically enqueue a partial upload. Re-upgrade restores usable defaults.
func TestMigrateDownImportStagingPreservesHistory(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	at := migrationIndex(migrations, "import_staging")
	if at < 0 {
		t.Fatal("no import_staging migration")
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	jobs := []ImportJob{
		{ID: "upload", BatchID: "b", CreatedBy: "alice", SourceName: "a.opus", StagedPath: ".CassiniImports/a.opus", State: importStateUploading, ExpectedBytes: 10, StagingState: "pending"},
		{ID: "success", BatchID: "b", CreatedBy: "alice", SourceName: "b.opus", StagedPath: ".CassiniImports/b.opus"},
	}
	if err := store.CreateImportJobs(ctx, jobs); err != nil {
		t.Fatal(err)
	}
	if err := store.SetImportJobOutcome(ctx, "success", "success.opus", 7, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateDownTo(migrations[at].Version - 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`SELECT staging_state FROM import_jobs`); err == nil {
		t.Fatal("staging column survived rollback")
	}
	var state string
	if err := store.db.QueryRow(`SELECT state FROM import_jobs WHERE id='upload'`).Scan(&state); err != nil || state != "failed" {
		t.Fatal(state, err)
	}
	if err := store.ensureSchema(); err != nil {
		t.Fatal(err)
	}
	done, ok, err := store.ImportJob(ctx, "success")
	if err != nil || !ok || done.State != importStateDone || done.DeliveredName != "success.opus" {
		t.Fatal(done, err)
	}
	upload, ok, err := store.ImportJob(ctx, "upload")
	if err != nil || !ok || upload.State != importStateFailed || upload.ExpectedBytes != 0 || upload.StagingState != "" {
		t.Fatal(upload, err)
	}
}
