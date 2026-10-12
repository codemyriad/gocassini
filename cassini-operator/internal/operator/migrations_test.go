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
		"speaker_edits": true,
		"insight_name":  true,
	}
	newest := migrations[len(migrations)-1].Name
	if !covered[newest] {
		t.Fatalf("the newest migration %q has no rollback test in migrations_test.go; add one and list it here", newest)
	}
}
