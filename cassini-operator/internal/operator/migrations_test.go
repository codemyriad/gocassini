package operator

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationsRunContiguouslyThroughInsightRuns(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	last := migrations[len(migrations)-1]
	if last.Version != 8 || last.Name != "insight_runs" {
		t.Fatalf("last migration = %04d_%s, want 0008_insight_runs", last.Version, last.Name)
	}
}

// TestMigrateDownRemovesTheInsightTablesAndUpRestoresThem exercises 0008 the way
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
