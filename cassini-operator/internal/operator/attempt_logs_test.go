package operator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func getAttemptLog(t *testing.T, rt *Runtime, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	rt.jobDetailHandler(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func seedAttemptLog(t *testing.T, rt *Runtime, jobID, stage, body string) string {
	t.Helper()
	insertJob(t, rt.store.db, jobID, nowUTCString())
	path := attemptLogPath(rt.cfg.WorkRoot, jobID, 1, stage)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rt.store.SetAttemptStageLogPath(context.Background(), jobID, 1, stage, path); err != nil {
		t.Fatalf("SetAttemptStageLogPath() error = %v", err)
	}
	return path
}

// An administrator reads why a step failed from the Operator (D-816).
func TestAttemptLogServesTheStepsLog(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedAttemptLog(t, rt, "job-log", "build", "loading model\nCUDA error: out of memory\n")

	rec := getAttemptLog(t, rt, "/jobs/job-log/attempts/1/logs/build")
	if rec.Code != http.StatusOK || rec.Body.String() != "loading model\nCUDA error: out of memory\n" {
		t.Fatalf("log = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if rec.Header().Get("X-Cassini-Log-Truncated") != "" {
		t.Fatal("a short log was marked as cut")
	}

	rec = getAttemptLog(t, rt, "/jobs/job-log/attempts/1/logs/build?download=1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), `attachment; filename="job-log-attempt-1-build.log"`) {
		t.Fatalf("download = %d, disposition %q", rec.Code, rec.Header().Get("Content-Disposition"))
	}
}

// A long log shows its end, from the start of a line, and says it was cut;
// the download is whole.
func TestAttemptLogViewShowsTheEnd(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	line := strings.Repeat("x", 99) + "\n"
	body := strings.Repeat(line, maxAttemptLogView/len(line)+10) + "the failure\n"
	seedAttemptLog(t, rt, "job-long", "build", body)

	rec := getAttemptLog(t, rt, "/jobs/job-long/attempts/1/logs/build")
	got := rec.Body.String()
	if rec.Header().Get("X-Cassini-Log-Truncated") != "1" || len(got) > maxAttemptLogView || !strings.HasSuffix(got, "the failure\n") || !strings.HasPrefix(got, "x") {
		t.Fatalf("tail = %d bytes, truncated %q", len(got), rec.Header().Get("X-Cassini-Log-Truncated"))
	}
	rec = getAttemptLog(t, rt, "/jobs/job-long/attempts/1/logs/build?download=1")
	if rec.Body.Len() != len(body) {
		t.Fatalf("download = %d bytes, want the whole %d", rec.Body.Len(), len(body))
	}
}

func TestAttemptLogRefusals(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	path := seedAttemptLog(t, rt, "job-gone", "build", "done\n")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if rec := getAttemptLog(t, rt, "/jobs/job-gone/attempts/1/logs/build"); rec.Code != http.StatusGone {
		t.Fatalf("deleted log = %d, want 410", rec.Code)
	}
	if rec := getAttemptLog(t, rt, "/jobs/job-gone/attempts/1/logs/seal"); rec.Code != http.StatusNotFound {
		t.Fatalf("step with no log = %d, want 404", rec.Code)
	}
	if rec := getAttemptLog(t, rt, "/jobs/job-gone/attempts/7/logs/build"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown attempt = %d, want 404", rec.Code)
	}
	if rec := getAttemptLog(t, rt, "/jobs/job-gone/attempts/1/logs/..%2F..%2Fjobs.sqlite3"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown stage = %d, want 404", rec.Code)
	}

	// A recorded path outside the work root is never served.
	outside := filepath.Join(t.TempDir(), "elsewhere.log")
	if err := os.WriteFile(outside, []byte("not a log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rt.store.SetAttemptStageLogPath(context.Background(), "job-gone", 1, "publish", outside); err != nil {
		t.Fatal(err)
	}
	if rec := getAttemptLog(t, rt, "/jobs/job-gone/attempts/1/logs/publish"); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "not a log") {
		t.Fatalf("outside the work root = %d %q, want 404", rec.Code, rec.Body.String())
	}
	rec := httptest.NewRecorder()
	rt.jobDetailHandler(rec, httptest.NewRequest(http.MethodPost, "/jobs/job-gone/attempts/1/logs/build", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", rec.Code)
	}
}
