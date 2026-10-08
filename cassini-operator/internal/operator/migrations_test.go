package operator

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationsRunContiguouslyThroughSpeakerEdits(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	last := migrations[len(migrations)-1]
	if last.Version != 14 || last.Name != "speaker_edits" {
		t.Fatalf("last migration = %04d_%s, want 0014_speaker_edits", last.Version, last.Name)
	}
}

// TestMigrateDownRemovesSpeakerEditsAndUpRestoresThem: a rollback to the last
// release must leave job_attempts as that release reads it, and the speaker
// edits must come back on re-upgrade.
func TestMigrateDownRemovesSpeakerEditsAndUpRestoresThem(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	seedJobRow(t, store.db, seededJobRow{ID: "kept", Stage: "done", State: "succeeded", CreatedAt: "2026-10-01T10:00:00Z"})

	if err := store.migrateDownTo(13); err != nil {
		t.Fatalf("migrateDownTo(13) error = %v", err)
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
