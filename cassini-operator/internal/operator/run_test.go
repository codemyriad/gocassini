package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestOpenStoreEnsuresSchemaAndEmptyList(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))

	path := filepath.Join(t.TempDir(), "jobs.sqlite3")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	jobs, err := store.ListJobs(context.Background())
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected empty jobs list, got %d", len(jobs))
	}
}

func TestJobsHandlerReturnsEmptyArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.sqlite3")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	rec := httptest.NewRecorder()
	jobsHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var jobs []Job
	if err := json.Unmarshal(rec.Body.Bytes(), &jobs); err != nil {
		t.Fatalf("decode jobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected empty jobs list, got %d", len(jobs))
	}
}

func TestJobDetailHandlerReturnsNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.sqlite3")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	req := httptest.NewRequest(http.MethodGet, "/jobs/missing", nil)
	rec := httptest.NewRecorder()
	jobDetailHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestListJobsOrdersNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.sqlite3")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	insertJob(t, store.db, "01A", "2026-04-29T10:00:00Z")
	insertJob(t, store.db, "01B", "2026-04-29T11:00:00Z")

	jobs, err := store.ListJobs(context.Background())
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
	if jobs[0].ID != "01B" || jobs[1].ID != "01A" {
		t.Fatalf("unexpected order: %#v", jobs)
	}
}

func insertJob(t *testing.T, db *sql.DB, id, createdAt string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO jobs (
  id, provider, request_json, stage, state,
  created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id,
		"nextcloud-talk",
		`{"platform":"nextcloud-talk","url":"https://example.test/call"}`,
		"record",
		"queued",
		createdAt,
		createdAt,
	); err != nil {
		t.Fatalf("insert job %s: %v", id, err)
	}
}
