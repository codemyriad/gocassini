package operator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
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
	if versions := migrationVersions(t, store.db); len(versions) != 7 || versions[0] != 1 || versions[1] != 2 || versions[2] != 3 || versions[3] != 4 || versions[4] != 5 || versions[5] != 6 || versions[6] != 7 {
		t.Fatalf("expected migration versions [1 2 3 4 5 6 7], got %v", versions)
	}
	if !sqliteTableExists(t, store.db, "job_attempts") {
		t.Fatalf("expected job_attempts table to exist")
	}
}

func TestOpenStoreBaselinesLegacySchemaDatabase(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))

	path := filepath.Join(t.TempDir(), "jobs.sqlite3")
	seedLegacyV1Database(t, path)

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	if versions := migrationVersions(t, store.db); len(versions) != 7 || versions[0] != 1 || versions[1] != 2 || versions[2] != 3 || versions[3] != 4 || versions[4] != 5 || versions[5] != 6 || versions[6] != 7 {
		t.Fatalf("expected migration versions [1 2 3 4 5 6 7], got %v", versions)
	}
	job := mustGetJob(t, store, "legacy-job")
	if job.Provider != "nextcloud-talk" || job.Stage != "record" || job.State != "queued" {
		t.Fatalf("unexpected legacy job after baseline = %#v", job)
	}
	attempts, err := store.ListJobAttempts(context.Background(), "legacy-job")
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(attempts) != 1 || attempts[0].AttemptNumber != 1 || attempts[0].TriggerKind != "initial" {
		t.Fatalf("unexpected legacy attempts after baseline = %#v", attempts)
	}
}

func TestBuildRetryMigrationNormalizesLegacyQueueTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.sqlite3")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	seedJobRow(t, store.db, seededJobRow{ID: "legacy-time", Stage: "build", State: "queued", CreatedAt: "2026-08-28T10:00:00Z"})
	if _, err := store.db.Exec(`
UPDATE jobs
SET created_at = '2026-08-28T10:00:00.1Z',
    build_queued_at = '2026-08-28T10:00:00.8Z',
    seal_queued_at = '2026-08-28T10:00:00.12Z',
    publish_queued_at = '2026-08-28T10:00:00Z'
WHERE id = 'legacy-time';
UPDATE job_attempts
SET created_at = '2026-08-28T10:00:00.2Z',
    build_queued_at = '2026-08-28T10:00:00.85Z',
    seal_queued_at = '2026-08-28T10:00:00.123Z',
    publish_queued_at = '2026-08-28T10:00:00.1234Z'
WHERE job_id = 'legacy-time' AND attempt_number = 1;`); err != nil {
		t.Fatalf("seed legacy timestamps: %v", err)
	}

	// Re-run migration 0007 exactly as an installation upgrading from the last
	// release would. Its direct TEXT comparisons are only chronological if old
	// RFC3339Nano values are first made fixed-width.
	if err := store.migrateDownTo(6); err != nil {
		t.Fatalf("migrateDownTo(6): %v", err)
	}
	if err := store.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema(): %v", err)
	}

	var jobCreated, jobBuild, jobSeal, jobPublish string
	var attemptCreated, attemptBuild, attemptSeal, attemptPublish string
	var jobDeferrals, attemptDeferrals int
	if err := store.db.QueryRow(`
SELECT created_at, build_queued_at, seal_queued_at, publish_queued_at, build_deferral_count
FROM jobs WHERE id = 'legacy-time'`).Scan(&jobCreated, &jobBuild, &jobSeal, &jobPublish, &jobDeferrals); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`
SELECT created_at, build_queued_at, seal_queued_at, publish_queued_at, build_deferral_count
FROM job_attempts WHERE job_id = 'legacy-time' AND attempt_number = 1`).Scan(
		&attemptCreated, &attemptBuild, &attemptSeal, &attemptPublish, &attemptDeferrals,
	); err != nil {
		t.Fatal(err)
	}
	gotJob := []string{jobCreated, jobBuild, jobSeal, jobPublish}
	wantJob := []string{
		"2026-08-28T10:00:00.100000000Z",
		"2026-08-28T10:00:00.800000000Z",
		"2026-08-28T10:00:00.120000000Z",
		"2026-08-28T10:00:00.000000000Z",
	}
	gotAttempt := []string{attemptCreated, attemptBuild, attemptSeal, attemptPublish}
	wantAttempt := []string{
		"2026-08-28T10:00:00.200000000Z",
		"2026-08-28T10:00:00.850000000Z",
		"2026-08-28T10:00:00.123000000Z",
		"2026-08-28T10:00:00.123400000Z",
	}
	if strings.Join(gotJob, "|") != strings.Join(wantJob, "|") || strings.Join(gotAttempt, "|") != strings.Join(wantAttempt, "|") {
		t.Fatalf("normalized order timestamps = jobs %#v attempts %#v", gotJob, gotAttempt)
	}
	if jobDeferrals != 0 || attemptDeferrals != 0 {
		t.Fatalf("new deferral counters = %d / %d, want zero", jobDeferrals, attemptDeferrals)
	}

	planRows, err := store.db.Query(`
EXPLAIN QUERY PLAN
SELECT id, current_attempt_number, artifact_run_path, build_deferral_count
FROM jobs
WHERE stage = 'build' AND state = 'queued' AND artifact_run_path IS NOT NULL
  AND (build_retry_not_before IS NULL OR build_retry_not_before <= ?)
ORDER BY build_queued_at ASC, id ASC`, nowUTCString())
	if err != nil {
		t.Fatalf("explain build queue query: %v", err)
	}
	defer planRows.Close()
	var plan []string
	for planRows.Next() {
		var id, parent, unused int
		var detail string
		if err := planRows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	planText := strings.Join(plan, "\n")
	if !strings.Contains(planText, "jobs_build_queue_order") || strings.Contains(planText, "TEMP B-TREE") {
		t.Fatalf("build queue query does not use FIFO index without sorting:\n%s", planText)
	}
}

func TestStoreMigrateDownToRemovesJobsSchema(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))

	path := filepath.Join(t.TempDir(), "jobs.sqlite3")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	if err := store.migrateDownTo(0); err != nil {
		t.Fatalf("migrateDownTo(0) error = %v", err)
	}
	if exists := sqliteTableExists(t, store.db, "jobs"); exists {
		t.Fatalf("expected jobs table to be removed after down migration")
	}
	if versions := migrationVersions(t, store.db); len(versions) != 0 {
		t.Fatalf("expected no applied migration versions after down migration, got %v", versions)
	}
}

func TestOpenStoreRejectsUnknownAppliedMigrationVersion(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))

	path := filepath.Join(t.TempDir(), "jobs.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE schema_migrations (
  version INTEGER PRIMARY KEY NOT NULL,
  name TEXT NOT NULL,
  applied_at TEXT NOT NULL
);
INSERT INTO schema_migrations (version, name, applied_at)
VALUES (9999, 'future', '2026-04-30T00:00:00Z');
`); err != nil {
		t.Fatalf("seed schema_migrations: %v", err)
	}

	if _, err := OpenStore(path); err == nil || !strings.Contains(err.Error(), "unknown applied migration version 9999") {
		t.Fatalf("expected unknown applied migration error, got %v", err)
	}
}

func TestJobsHandlerReturnsEmptyArray(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)

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

func TestHTTPHandlerMountsRoutesUnderConfiguredBasePath(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.BasePath = "/operator"

	handler := newHTTPHandler(log.New(ioDiscard{}, "", 0), rt, ExAppConfig{})

	prefixedReq := httptest.NewRequest(http.MethodGet, "/operator/jobs", nil)
	prefixedRec := httptest.NewRecorder()
	handler.ServeHTTP(prefixedRec, prefixedReq)
	if prefixedRec.Code != http.StatusOK {
		t.Fatalf("prefixed status = %d, want %d body=%s", prefixedRec.Code, http.StatusOK, prefixedRec.Body.String())
	}

	rootReq := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	rootRec := httptest.NewRecorder()
	handler.ServeHTTP(rootRec, rootReq)
	if rootRec.Code != http.StatusNotFound {
		t.Fatalf("root status = %d, want %d body=%s", rootRec.Code, http.StatusNotFound, rootRec.Body.String())
	}
}

func TestJobDetailHandlerReturnsNotFound(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/jobs/missing", nil)
	rec := httptest.NewRecorder()
	rt.jobDetailHandler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestListJobsOrdersNewestFirst(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	insertJob(t, rt.store.db, "01A", "2026-04-29T10:00:00Z")
	insertJob(t, rt.store.db, "01B", "2026-04-29T11:00:00Z")

	jobs, err := rt.store.ListJobs(context.Background())
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

func TestLoadConfigRejectsMissingCassiniBin(t *testing.T) {
	repoRoot := makeFakeOperatorRepoRoot(t)
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)
	t.Setenv("CASSINI_BIN", filepath.Join(repoRoot, "bin", "missing-cassini"))

	_, exitCode, err := loadConfig(nil, ioDiscard{})
	if err == nil {
		t.Fatalf("expected loadConfig error")
	}
	if exitCode != 2 {
		t.Fatalf("exitCode = %d, want 2 err=%v", exitCode, err)
	}
	if !strings.Contains(err.Error(), "cassini binary") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunStartupReportsResolvedExAppPublishSink(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)

	repoRoot := makeFakeOperatorRepoRoot(t)
	dataRoot := t.TempDir()
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)
	t.Setenv(envAppID, "gocassini")
	t.Setenv(envAppVersion, "test")
	t.Setenv(envAppSecret, "test-app-secret")
	t.Setenv(envNextcloudURL, "https://cloud.example.test")
	t.Setenv(envAppHost, "")
	t.Setenv(envAppPort, "")
	t.Setenv(envAppPersistentStorage, "")
	t.Setenv(envAppAPIRequired, "false")
	t.Setenv(envViewerDist, "")
	t.Setenv(envPublishSinkName, "")
	t.Setenv(envSTTCUDACapable, "0")
	t.Setenv("CASSINI_TALK_RECORDING_SECRET", "test-recording-secret")
	t.Setenv(envTalkSignalingInternalSecret, "test-signaling-secret")

	args := []string{
		"--bind", "127.0.0.1:0",
		"--db", filepath.Join(dataRoot, "jobs.sqlite3"),
		"--work-root", filepath.Join(dataRoot, "jobs"),
		"--site-root", filepath.Join(dataRoot, "site"),
		"--cassini-bin", filepath.Join(repoRoot, "bin", "cassini"),
	}
	cfg, exitCode, err := loadConfig(args, ioDiscard{})
	if err != nil || exitCode != 0 {
		t.Fatalf("loadConfig() exitCode = %d err = %v", exitCode, err)
	}
	if cfg.PublishSink != "" {
		t.Fatalf("raw PublishSink = %q, want empty (unset)", cfg.PublishSink)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, args, &stdout, &stderr); code != 0 {
		t.Fatalf("Run() = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}

	logs := stderr.String()
	if got := strings.Count(logs, "publish_sink ->"); got != 1 {
		t.Fatalf("publish sink startup summaries = %d, want 1\nlogs:\n%s", got, logs)
	}
	if !strings.Contains(logs, "publish_sink -> "+publishSinkNextcloudFiles) {
		t.Fatalf("startup summary does not report resolved ExApp sink %q\nlogs:\n%s", publishSinkNextcloudFiles, logs)
	}
	if strings.Contains(logs, "publish_sink -> "+publishSinkLocal) {
		t.Fatalf("startup summary reports standalone sink for an unset ExApp config\nlogs:\n%s", logs)
	}
}

func TestLoadConfigUsesCassiniPrefixedWorkerEnvAndBasePath(t *testing.T) {
	repoRoot := makeFakeOperatorRepoRoot(t)
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)
	t.Setenv("CASSINI_MAX_RECORD_WORKERS", "2")
	t.Setenv("CASSINI_MAX_BUILD_WORKERS", "3")
	t.Setenv("CASSINI_OPERATOR_BASE_PATH", "/operator/")

	cfg, exitCode, err := loadConfig(nil, ioDiscard{})
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if cfg.BindAddr != "0.0.0.0:4000" {
		t.Fatalf("bind = %q, want %q", cfg.BindAddr, "0.0.0.0:4000")
	}
	if cfg.BasePath != "/operator" {
		t.Fatalf("basePath = %q, want %q", cfg.BasePath, "/operator")
	}
	if cfg.MaxRecordWorkers != 2 {
		t.Fatalf("maxRecordWorkers = %d, want 2", cfg.MaxRecordWorkers)
	}
	if cfg.MaxBuildWorkers != 3 {
		t.Fatalf("maxBuildWorkers = %d, want 3", cfg.MaxBuildWorkers)
	}
}

func TestLoadConfigAllowsExplicitPathsOutsideRepo(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	defer func() {
		_ = os.Chdir(cwd)
	}()

	cassiniBin := filepath.Join(tmp, "cassini")
	if err := os.WriteFile(cassiniBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write cassini bin: %v", err)
	}
	t.Setenv("CASSINI_BIN", cassiniBin)
	t.Setenv("CASSINI_OPERATOR_DB_PATH", filepath.Join(tmp, "runtime", "jobs.sqlite3"))
	t.Setenv("CASSINI_OPERATOR_WORK_ROOT", filepath.Join(tmp, "runtime", "jobs"))
	t.Setenv("CASSINI_OPERATOR_SITE_ROOT", filepath.Join(tmp, "runtime", "site"))

	cfg, exitCode, err := loadConfig(nil, ioDiscard{})
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if cfg.RepoRoot != "" {
		t.Fatalf("repoRoot = %q, want empty outside repo", cfg.RepoRoot)
	}
	if cfg.CassiniBin != cassiniBin {
		t.Fatalf("cassiniBin = %q, want %q", cfg.CassiniBin, cassiniBin)
	}
	if cfg.DBPath != filepath.Join(tmp, "runtime", "jobs.sqlite3") {
		t.Fatalf("dbPath = %q", cfg.DBPath)
	}
}

func TestLoadConfigRedirectsBakedDefaultsUnderPersistentStorage(t *testing.T) {
	// Simulate the ExApp container: AppAPI mounted its persistent volume and
	// the image baked the ephemeral data paths into env (Dockerfile.exapp).
	// All three roots must redirect under APP_PERSISTENT_STORAGE.
	repoRoot := makeFakeOperatorRepoRoot(t)
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)
	t.Setenv("APP_PERSISTENT_STORAGE", "/nc_app_gocassini_data")
	t.Setenv("CASSINI_OPERATOR_DB_PATH", "/var/lib/cassini-operator/jobs.sqlite3")
	t.Setenv("CASSINI_OPERATOR_WORK_ROOT", "/var/lib/cassini-operator/jobs")
	t.Setenv("CASSINI_OPERATOR_SITE_ROOT", "/srv/cassini-site/published")

	cfg, exitCode, err := loadConfig(nil, ioDiscard{})
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if cfg.DBPath != "/nc_app_gocassini_data/operator/jobs.sqlite3" {
		t.Fatalf("dbPath = %q, want under APP_PERSISTENT_STORAGE", cfg.DBPath)
	}
	if cfg.WorkRoot != "/nc_app_gocassini_data/operator/jobs" {
		t.Fatalf("workRoot = %q, want under APP_PERSISTENT_STORAGE", cfg.WorkRoot)
	}
	if cfg.SiteRoot != "/nc_app_gocassini_data/site/published" {
		t.Fatalf("siteRoot = %q, want under APP_PERSISTENT_STORAGE", cfg.SiteRoot)
	}
}

func TestLoadConfigKeepsExplicitPathsDespitePersistentStorage(t *testing.T) {
	// Admin override: paths set to anything other than the baked image
	// defaults stay untouched even when APP_PERSISTENT_STORAGE is mounted.
	repoRoot := makeFakeOperatorRepoRoot(t)
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)
	t.Setenv("APP_PERSISTENT_STORAGE", "/nc_app_gocassini_data")
	t.Setenv("CASSINI_OPERATOR_DB_PATH", "/mnt/big-disk/jobs.sqlite3")
	t.Setenv("CASSINI_OPERATOR_WORK_ROOT", "/mnt/big-disk/jobs")
	t.Setenv("CASSINI_OPERATOR_SITE_ROOT", "/mnt/big-disk/site/published")

	cfg, exitCode, err := loadConfig(nil, ioDiscard{})
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if cfg.DBPath != "/mnt/big-disk/jobs.sqlite3" {
		t.Fatalf("dbPath = %q, want explicit override", cfg.DBPath)
	}
	if cfg.WorkRoot != "/mnt/big-disk/jobs" {
		t.Fatalf("workRoot = %q, want explicit override", cfg.WorkRoot)
	}
	if cfg.SiteRoot != "/mnt/big-disk/site/published" {
		t.Fatalf("siteRoot = %q, want explicit override", cfg.SiteRoot)
	}
}

func TestStartupMarksIncompleteJobsInterruptedAndPreservesStage(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	seedJobRow(t, rt.store.db, seededJobRow{ID: "queued-record", Stage: "record", State: "queued", CreatedAt: "2026-04-29T10:00:00Z"})
	seedJobRow(t, rt.store.db, seededJobRow{ID: "running-build", Stage: "build", State: "running", CreatedAt: "2026-04-29T10:01:00Z"})
	seedJobRow(t, rt.store.db, seededJobRow{ID: "queued-build", Stage: "build", State: "queued", CreatedAt: "2026-04-29T10:01:30Z"})
	seedJobRow(t, rt.store.db, seededJobRow{ID: "queued-publish", Stage: "publish", State: "queued", CreatedAt: "2026-04-29T10:02:00Z"})
	seedJobRow(t, rt.store.db, seededJobRow{ID: "blocked-build", Stage: "build", State: "blocked", CreatedAt: "2026-04-29T10:02:30Z"})
	seedJobRow(t, rt.store.db, seededJobRow{ID: "done-success", Stage: "done", State: "succeeded", CreatedAt: "2026-04-29T10:03:00Z", CompletedAt: strPtr("2026-04-29T10:04:00Z")})
	seedJobRow(t, rt.store.db, seededJobRow{ID: "done-failed", Stage: "done", State: "failed", CreatedAt: "2026-04-29T10:05:00Z", CompletedAt: strPtr("2026-04-29T10:06:00Z")})

	interruptedAt := "2026-04-29T11:00:00Z"
	count, err := rt.store.MarkIncompleteJobsInterrupted(context.Background(), interruptedAt)
	if err != nil {
		t.Fatalf("MarkIncompleteJobsInterrupted() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}

	queuedRecord := mustGetJob(t, rt.store, "queued-record")
	if queuedRecord.Stage != "record" || queuedRecord.State != "interrupted" {
		t.Fatalf("unexpected queued record job = %#v", queuedRecord)
	}
	if queuedRecord.InterruptedAt == nil || *queuedRecord.InterruptedAt != interruptedAt {
		t.Fatalf("unexpected interrupted_at for queued record = %#v", queuedRecord.InterruptedAt)
	}
	queuedRecordAttempts, err := rt.store.ListJobAttempts(context.Background(), "queued-record")
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(queuedRecordAttempts) != 1 || queuedRecordAttempts[0].State != "interrupted" {
		t.Fatalf("unexpected queued-record attempts = %#v", queuedRecordAttempts)
	}

	runningBuild := mustGetJob(t, rt.store, "running-build")
	if runningBuild.Stage != "build" || runningBuild.State != "interrupted" {
		t.Fatalf("unexpected running build job = %#v", runningBuild)
	}

	// Queued build/publish rows survive a restart untouched: their inputs are
	// durable on disk and the requeue dispatcher re-delivers them (D-367).
	queuedBuild := mustGetJob(t, rt.store, "queued-build")
	if queuedBuild.Stage != "build" || queuedBuild.State != "queued" || queuedBuild.InterruptedAt != nil {
		t.Fatalf("queued build job should stay queued, got %#v", queuedBuild)
	}

	queuedPublish := mustGetJob(t, rt.store, "queued-publish")
	if queuedPublish.Stage != "publish" || queuedPublish.State != "queued" || queuedPublish.InterruptedAt != nil {
		t.Fatalf("queued publish job should stay queued, got %#v", queuedPublish)
	}

	blockedBuild := mustGetJob(t, rt.store, "blocked-build")
	if blockedBuild.Stage != "build" || blockedBuild.State != "blocked" || blockedBuild.InterruptedAt != nil {
		t.Fatalf("blocked build job should remain recoverable after restart, got %#v", blockedBuild)
	}

	doneSuccess := mustGetJob(t, rt.store, "done-success")
	if doneSuccess.State != "succeeded" || doneSuccess.InterruptedAt != nil {
		t.Fatalf("completed success should be unchanged, got %#v", doneSuccess)
	}

	doneFailed := mustGetJob(t, rt.store, "done-failed")
	if doneFailed.State != "failed" || doneFailed.InterruptedAt != nil {
		t.Fatalf("completed failed should be unchanged, got %#v", doneFailed)
	}
}

func TestCreateJobReturnsULIDAndCompletesPublishStageWithCanonicalAndAttemptArtifacts(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/call"}`))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp createJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, err := ulid.Parse(resp.ID); err != nil {
		t.Fatalf("response id %q is not a ulid: %v", resp.ID, err)
	}

	job := waitForJobState(t, rt.store, resp.ID, "succeeded")
	if job.CurrentAttemptNumber != 1 {
		t.Fatalf("current_attempt_number = %d, want 1", job.CurrentAttemptNumber)
	}
	if job.RerunCount != 0 {
		t.Fatalf("rerun_count = %d, want 0", job.RerunCount)
	}
	if job.Stage != "done" {
		t.Fatalf("stage = %q, want done", job.Stage)
	}
	if job.ArtifactRunPath == nil {
		t.Fatalf("expected canonical artifact_run_path to be set")
	}
	if !strings.Contains(*job.ArtifactRunPath, filepath.Join("current", resp.ID+".run")) {
		t.Fatalf("expected canonical current run path, got %#v", job.ArtifactRunPath)
	}
	if job.ArtifactMeetingPath == nil {
		t.Fatalf("expected canonical artifact_meeting_path to be set")
	}
	if !strings.Contains(*job.ArtifactMeetingPath, filepath.Join("current", resp.ID+".meeting")) {
		t.Fatalf("expected canonical current meeting path, got %#v", job.ArtifactMeetingPath)
	}
	if job.ArtifactSitePath == nil {
		t.Fatalf("expected artifact_site_path to be set")
	}
	if *job.ArtifactSitePath != rt.cfg.SiteRoot {
		t.Fatalf("expected shared live site path, got %#v want %q", job.ArtifactSitePath, rt.cfg.SiteRoot)
	}
	liveSiteManifest, ok, err := LoadSiteBundleManifest(*job.ArtifactSitePath)
	if err != nil {
		t.Fatalf("LoadSiteBundleManifest() error = %v", err)
	}
	if !ok {
		t.Fatalf("expected live site bundle manifest at %s", *job.ArtifactSitePath)
	}
	if liveSiteManifest.PublishedByJobID != resp.ID || liveSiteManifest.PublishedByAttemptNumber != 1 {
		t.Fatalf("unexpected live site lineage = %#v", liveSiteManifest)
	}
	if strings.TrimSpace(liveSiteManifest.PublishedAtUTC) == "" {
		t.Fatalf("expected live site published_at_utc in manifest = %#v", liveSiteManifest)
	}
	if job.BuildQueuedAt == nil || job.BuildStartedAt == nil || job.BuildFinishedAt == nil {
		t.Fatalf("expected build timestamps to be set, got job=%#v", job)
	}
	if job.PublishQueuedAt == nil || job.PublishStartedAt == nil || job.PublishFinishedAt == nil {
		t.Fatalf("expected publish timestamps to be set, got job=%#v", job)
	}
	if job.StopReason == nil || *job.StopReason != "room_empty" {
		t.Fatalf("expected room_empty stop reason, got %#v", job.StopReason)
	}
	if job.RecordExitCode == nil || *job.RecordExitCode != 0 {
		t.Fatalf("expected record exit code 0, got %#v", job.RecordExitCode)
	}
	if job.RecordStopDetail == nil || !strings.Contains(*job.RecordStopDetail, "room empty") {
		t.Fatalf("expected room empty stop detail, got %#v", job.RecordStopDetail)
	}
	if _, err := os.Stat(filepath.Join(*job.ArtifactRunPath, "recording.mkv")); err != nil {
		t.Fatalf("expected canonical recording.mkv in run bundle: %v", err)
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(attempts))
	}
	if attempts[0].AttemptNumber != 1 || attempts[0].TriggerKind != "initial" {
		t.Fatalf("unexpected first attempt = %#v", attempts[0])
	}
	if attempts[0].ArtifactRunPath == nil {
		t.Fatalf("expected retained attempt-local artifact_run_path on attempt 1")
	}
	if !strings.Contains(*attempts[0].ArtifactRunPath, filepath.Join("runs", resp.ID+"--attempt-001.run")) {
		t.Fatalf("expected retained attempt-local run path, got %#v", attempts[0].ArtifactRunPath)
	}
	if attempts[0].ArtifactMeetingPath == nil {
		t.Fatalf("expected attempt-local artifact_meeting_path on attempt 1")
	}
	if !strings.Contains(*attempts[0].ArtifactMeetingPath, filepath.Join("runs", resp.ID+"--attempt-001.meeting")) {
		t.Fatalf("expected attempt-local meeting path, got %#v", attempts[0].ArtifactMeetingPath)
	}
	if attempts[0].ArtifactSitePath == nil {
		t.Fatalf("expected attempt-local artifact_site_path on attempt 1")
	}
	if !strings.Contains(*attempts[0].ArtifactSitePath, filepath.Join("runs", resp.ID+"--attempt-001.site")) {
		t.Fatalf("expected retained attempt-local site path, got %#v", attempts[0].ArtifactSitePath)
	}
	if attempts[0].RequestJSON != job.RequestJSON {
		t.Fatalf("attempt request_json mismatch: attempt=%s job=%s", attempts[0].RequestJSON, job.RequestJSON)
	}
}

func TestJobDetailHandlerIncludesStopMetadata(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	createReq := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/read-surface"}`))
	createRec := httptest.NewRecorder()
	rt.jobsHandler(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d body=%s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var resp createJobResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	_ = waitForJobState(t, rt.store, resp.ID, "succeeded")

	detailReq := httptest.NewRequest(http.MethodGet, "/jobs/"+resp.ID, nil)
	detailRec := httptest.NewRecorder()
	rt.jobDetailHandler(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want %d body=%s", detailRec.Code, http.StatusOK, detailRec.Body.String())
	}
	var detail jobDetailResponse
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail response: %v", err)
	}
	if detail.Job.ID != resp.ID {
		t.Fatalf("detail job id = %q, want %q", detail.Job.ID, resp.ID)
	}
	if detail.Job.StopReason == nil || *detail.Job.StopReason != "room_empty" {
		t.Fatalf("expected stop_reason on detail job, got %#v", detail.Job.StopReason)
	}
	if len(detail.Attempts) != 1 {
		t.Fatalf("expected 1 attempt in detail response, got %d", len(detail.Attempts))
	}
	if detail.Attempts[0].AttemptNumber != 1 {
		t.Fatalf("expected attempt 1 in detail response, got %#v", detail.Attempts[0])
	}
}

func TestJobDetailHandlerIncludesAttemptHistoryAfterRerun(t *testing.T) {
	rt, cleanup, _, _ := newCLITestRuntime(t)
	defer cleanup()

	buildCalls := 0
	rt.buildJobFn = func(ctx context.Context, task buildTask) (string, error) {
		buildCalls++
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if buildCalls == 1 {
			if err := os.MkdirAll(meetingPath, 0o755); err != nil {
				return meetingPath, err
			}
			manifest := `{
  "kind": "meeting",
  "version": "cassini.meeting.v1",
  "state": "failed",
  "stage": "build",
  "error": "transcriber exploded"
}`
			if err := os.WriteFile(filepath.Join(meetingPath, "cassini.json"), []byte(manifest), 0o644); err != nil {
				return meetingPath, err
			}
			return meetingPath, errors.New("exit status 1")
		}
		if err := writeReadyMeetingBundleFixture(meetingPath, task.ArtifactRunPath); err != nil {
			return meetingPath, err
		}
		return meetingPath, nil
	}

	createReq := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/detail-rerun"}`))
	createRec := httptest.NewRecorder()
	rt.jobsHandler(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d body=%s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var createResp createJobResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	_ = waitForJobState(t, rt.store, createResp.ID, "failed")

	rerunReq := httptest.NewRequest(http.MethodPost, "/jobs/"+createResp.ID+"/rerun", nil)
	rerunRec := httptest.NewRecorder()
	rt.jobDetailHandler(rerunRec, rerunReq)
	if rerunRec.Code != http.StatusAccepted {
		t.Fatalf("rerun status = %d, want %d body=%s", rerunRec.Code, http.StatusAccepted, rerunRec.Body.String())
	}
	_ = waitForJobState(t, rt.store, createResp.ID, "succeeded")

	detailReq := httptest.NewRequest(http.MethodGet, "/jobs/"+createResp.ID, nil)
	detailRec := httptest.NewRecorder()
	rt.jobDetailHandler(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want %d body=%s", detailRec.Code, http.StatusOK, detailRec.Body.String())
	}
	var detail jobDetailResponse
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail response: %v", err)
	}
	if detail.Job.CurrentAttemptNumber != 2 {
		t.Fatalf("current_attempt_number = %d, want 2", detail.Job.CurrentAttemptNumber)
	}
	if detail.Job.RerunCount != 1 {
		t.Fatalf("rerun_count = %d, want 1", detail.Job.RerunCount)
	}
	if detail.Job.ArtifactRunPath == nil || !strings.Contains(*detail.Job.ArtifactRunPath, filepath.Join("current", createResp.ID+".run")) {
		t.Fatalf("expected canonical run path on detail job, got %#v", detail.Job.ArtifactRunPath)
	}
	if len(detail.Attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(detail.Attempts))
	}
	if detail.Attempts[0].AttemptNumber != 2 || detail.Attempts[0].State != "succeeded" {
		t.Fatalf("unexpected latest attempt = %#v", detail.Attempts[0])
	}
	if detail.Attempts[1].AttemptNumber != 1 || detail.Attempts[1].State != "failed" {
		t.Fatalf("unexpected original failed attempt = %#v", detail.Attempts[1])
	}
	if detail.Attempts[0].ArtifactRunPath == nil || !strings.Contains(*detail.Attempts[0].ArtifactRunPath, filepath.Join("current", createResp.ID+".run")) {
		t.Fatalf("expected canonical run source on rerun attempt, got %#v", detail.Attempts[0].ArtifactRunPath)
	}
	if detail.Attempts[0].RecordQueuedAt != nil || detail.Attempts[0].RecordStartedAt != nil || detail.Attempts[0].RecordFinishedAt != nil {
		t.Fatalf("did not expect record timestamps on rerun attempt, got %#v", detail.Attempts[0])
	}
	if detail.Attempts[0].RecordLogPath != nil {
		t.Fatalf("did not expect record log path on rerun attempt, got %#v", detail.Attempts[0].RecordLogPath)
	}
	if detail.Attempts[0].BuildQueuedAt == nil || detail.Attempts[0].BuildStartedAt == nil || detail.Attempts[0].BuildFinishedAt == nil {
		t.Fatalf("expected build timestamps on rerun attempt, got %#v", detail.Attempts[0])
	}
	if detail.Attempts[0].PublishQueuedAt == nil || detail.Attempts[0].PublishStartedAt == nil || detail.Attempts[0].PublishFinishedAt == nil {
		t.Fatalf("expected publish timestamps on rerun attempt, got %#v", detail.Attempts[0])
	}
	if detail.Attempts[1].Error == nil || *detail.Attempts[1].Error == "" {
		t.Fatalf("expected persisted error on first failed attempt, got %#v", detail.Attempts[1].Error)
	}
}

func TestCreateJobRunsDoctorAndRecordWithNormalizedDefaults(t *testing.T) {
	rt, cleanup, logPath, _ := newCLITestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/live"}`))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var resp createJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	job := waitForJobState(t, rt.store, resp.ID, "succeeded")

	if !strings.Contains(job.RequestJSON, `"guestName":"CassiniRecorder"`) {
		t.Fatalf("expected normalized guestName in request_json, got %s", job.RequestJSON)
	}
	if !strings.Contains(job.RequestJSON, `"stopWhenRoomEmpty":true`) {
		t.Fatalf("expected normalized stopWhenRoomEmpty in request_json, got %s", job.RequestJSON)
	}
	if !strings.Contains(job.RequestJSON, `"roomEmptyGrace":30`) {
		t.Fatalf("expected normalized roomEmptyGrace in request_json, got %s", job.RequestJSON)
	}
	if !strings.Contains(job.RequestJSON, `"talkAuthMode":"hpb-internal"`) {
		t.Fatalf("expected normalized talkAuthMode in request_json, got %s", job.RequestJSON)
	}
	logText := readFileString(t, logPath)
	if !strings.Contains(logText, "doctor --target record") {
		t.Fatalf("expected doctor invocation, got %s", logText)
	}
	if !strings.Contains(logText, "--call https://example.test/live") {
		t.Fatalf("expected record invocation, got %s", logText)
	}
	if !strings.Contains(logText, "--name CassiniRecorder") {
		t.Fatalf("expected default guest name flag, got %s", logText)
	}
	if !strings.Contains(logText, "--talk-auth-mode hpb-internal") {
		t.Fatalf("expected talk auth mode flag, got %s", logText)
	}
	if strings.Contains(logText, "--duration") {
		t.Fatalf("did not expect duration flag by default, got %s", logText)
	}
	if strings.Contains(logText, "--room-empty-grace") {
		t.Fatalf("did not expect room-empty-grace flag by default, got %s", logText)
	}
	if strings.Contains(logText, "--stop-when-room-empty=") {
		t.Fatalf("did not expect explicit stop-when-room-empty flag by default, got %s", logText)
	}
}

func TestCreateJobPassesExplicitRecordOptions(t *testing.T) {
	rt, cleanup, logPath, _ := newCLITestRuntime(t)
	defer cleanup()

	body := `{"platform":"nextcloud-talk","url":"https://example.test/live","guestName":"Guesty","duration":12,"stopWhenRoomEmpty":false,"roomEmptyGrace":7.5}`
	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(body))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var resp createJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	_ = waitForJobState(t, rt.store, resp.ID, "succeeded")

	logText := readFileString(t, logPath)
	for _, want := range []string{
		"--name Guesty",
		"--talk-auth-mode hpb-internal",
		"--duration 12",
		"--stop-when-room-empty=false",
		"--room-empty-grace 7.5",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected %q in log, got %s", want, logText)
		}
	}
}

func TestCreateJobAcceptsExplicitTalkTargetWithoutURL(t *testing.T) {
	rt, cleanup, logPath, _ := newCLITestRuntime(t)
	defer cleanup()

	body := `{"platform":"nextcloud-talk","baseURL":"https://example.test/","roomToken":"room-42","talkAuthMode":"hpb-internal"}`
	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(body))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var resp createJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	job := waitForJobState(t, rt.store, resp.ID, "succeeded")
	if !strings.Contains(job.RequestJSON, `"baseURL":"https://example.test"`) {
		t.Fatalf("expected normalized baseURL in request_json, got %s", job.RequestJSON)
	}
	if !strings.Contains(job.RequestJSON, `"roomToken":"room-42"`) {
		t.Fatalf("expected roomToken in request_json, got %s", job.RequestJSON)
	}
	if !strings.Contains(job.RequestJSON, `"talkAuthMode":"hpb-internal"`) {
		t.Fatalf("expected talkAuthMode in request_json, got %s", job.RequestJSON)
	}

	logText := readFileString(t, logPath)
	for _, want := range []string{
		"record --out",
		"--call https://example.test/call/room-42",
		"--talk-base-url https://example.test",
		"--talk-room-token room-42",
		"--talk-auth-mode hpb-internal",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected %q in log, got %s", want, logText)
		}
	}
}

func TestCreateJobHonorsExplicitGuestFallbackTalkAuthMode(t *testing.T) {
	rt, cleanup, logPath, _ := newCLITestRuntime(t)
	defer cleanup()

	body := `{"platform":"nextcloud-talk","url":"https://example.test/live","talkAuthMode":"guest-participant"}`
	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(body))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var resp createJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	job := waitForJobState(t, rt.store, resp.ID, "succeeded")
	if !strings.Contains(job.RequestJSON, `"talkAuthMode":"guest-participant"`) {
		t.Fatalf("expected explicit guest fallback in request_json, got %s", job.RequestJSON)
	}
	logText := readFileString(t, logPath)
	if !strings.Contains(logText, "--talk-auth-mode guest-participant") {
		t.Fatalf("expected explicit guest fallback flag, got %s", logText)
	}
}

func TestStopJobAcceptsRunningRecordAndCompletesPublishStage(t *testing.T) {
	rt, cleanup, logPath, startedPath := newCLITestRuntime(t)
	defer cleanup()
	t.Setenv("FAKE_RECORD_WAIT_FOR_SIGNAL", "1")

	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/stop-me"}`))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var resp createJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	waitForFile(t, startedPath)
	waitForRecordState(t, rt.store, resp.ID, "running")

	stopReq := httptest.NewRequest(http.MethodPost, "/jobs/"+resp.ID+"/stop", nil)
	stopRec := httptest.NewRecorder()
	rt.jobDetailHandler(stopRec, stopReq)
	if stopRec.Code != http.StatusAccepted {
		t.Fatalf("first stop status = %d, want %d body=%s", stopRec.Code, http.StatusAccepted, stopRec.Body.String())
	}

	stopReq2 := httptest.NewRequest(http.MethodPost, "/jobs/"+resp.ID+"/stop", nil)
	stopRec2 := httptest.NewRecorder()
	rt.jobDetailHandler(stopRec2, stopReq2)
	// The second stop races the job's own progression: 202 while the job is
	// still stopping, or 409 once it has already left the stoppable state
	// (the fake record proceeds to build/publish after the first stop signal).
	// Both are correct outcomes; only an unexpected code is a real failure.
	// A strict ==202 assertion flakes under CI load, when the gap between the
	// two stops is wide enough for the job to finish stopping first.
	if stopRec2.Code != http.StatusAccepted && stopRec2.Code != http.StatusConflict {
		t.Fatalf("second stop status = %d, want 202 or 409 body=%s", stopRec2.Code, stopRec2.Body.String())
	}
	// A 409 is only legitimate here when the job has genuinely left the record
	// stage — i.e. the store predicate refused it. It must never be the
	// registration guard: that would mean the store still reports record/running
	// while the map entry is gone, which is the D-501 invariant violation this
	// blanket tolerance used to be able to absorb. The store only moves forward,
	// so this is race-free and needs no re-read.
	if stopRec2.Code == http.StatusConflict && strings.Contains(stopRec2.Body.String(), "no record registration") {
		t.Fatalf("second stop 409'd on a missing registration, want the store predicate: %s", stopRec2.Body.String())
	}

	job := waitForJobState(t, rt.store, resp.ID, "succeeded")
	if job.Stage != "done" {
		t.Fatalf("stage = %q, want done", job.Stage)
	}
	if job.ArtifactRunPath == nil || !strings.Contains(*job.ArtifactRunPath, filepath.Join("current", resp.ID+".run")) {
		t.Fatalf("expected canonical current run path, got %#v", job.ArtifactRunPath)
	}
	if job.ArtifactMeetingPath == nil || !strings.Contains(*job.ArtifactMeetingPath, filepath.Join("current", resp.ID+".meeting")) {
		t.Fatalf("expected canonical current meeting path, got %#v", job.ArtifactMeetingPath)
	}
	if job.ArtifactSitePath == nil {
		t.Fatalf("expected artifact_site_path after publish, got job=%#v", job)
	}
	if job.StopReason == nil || *job.StopReason != "operator_requested" {
		t.Fatalf("expected operator_requested stop reason, got %#v", job.StopReason)
	}
	if job.StopRequestedAt == nil || job.StopSignalSentAt == nil {
		t.Fatalf("expected stop request timestamps, got job=%#v", job)
	}
	if job.RecordExitCode == nil || *job.RecordExitCode != 0 {
		t.Fatalf("expected record exit code 0, got %#v", job.RecordExitCode)
	}
	logText := readFileString(t, logPath)
	if !strings.Contains(logText, "--call https://example.test/stop-me") {
		t.Fatalf("expected record invocation, got %s", logText)
	}
}

// TestStopJobAcceptedBeforeRecordProcessSpawned pins the D-501 window
// deterministically. The recordJobFn seam is the repro: a fake that blocks
// without ever spawning a recorder holds the job *permanently* in the state the
// flake hit only by chance — the store says record/running, the process map is
// empty. Before the bracket in runRecordJob, the stop handler's map gate
// answered 409 "job is not stoppable" against a job the API had just reported
// as running. No sleep, no stress loop, no timing assumption.
func TestStopJobAcceptedBeforeRecordProcessSpawned(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	release := make(chan struct{})
	rt.recordJobFn = func(ctx context.Context, job Job, req TriggerRequest) (recordResult, error) {
		<-release
		runPath := attemptRunPath(rt.cfg.WorkRoot, job.ID, job.CurrentAttemptNumber)
		bundle, err := PrepareRunBundle(runPath, false)
		if err != nil {
			return recordResult{}, err
		}
		if err := os.WriteFile(bundle.RecordingPath, []byte("fake-mkv"), 0o644); err != nil {
			return recordResult{}, err
		}
		if err := FinalizeRunBundle(bundle, RunManifest{SourceMode: "talk", RecorderName: req.GuestName}); err != nil {
			return recordResult{}, err
		}
		return recordResult{ArtifactRunPath: bundle.RootDir, StopReason: "operator_requested", ExitCode: intPtr(0)}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/pre-spawn"}`))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp createJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	waitForRecordState(t, rt.store, resp.ID, "running")

	stopReq := httptest.NewRequest(http.MethodPost, "/jobs/"+resp.ID+"/stop", nil)
	stopRec := httptest.NewRecorder()
	rt.jobDetailHandler(stopRec, stopReq)
	if stopRec.Code != http.StatusAccepted {
		t.Fatalf("stop before spawn status = %d, want %d body=%s", stopRec.Code, http.StatusAccepted, stopRec.Body.String())
	}

	// The 202 is only honest if the stop was actually recorded: an accepted
	// stop that writes nothing is a silent no-op, which is worse than the 409.
	job, err := rt.store.GetJob(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if job.StopRequestedAt == nil {
		t.Fatalf("accepted stop did not record stop_requested_at, got job=%#v", job)
	}

	close(release)
	_ = waitForJobState(t, rt.store, resp.ID, "succeeded")
}

// TestStopRequestedBeforeSpawnIsDeliveredOnAttach covers the other half of the
// promise the 202 above makes. The handler accepts the stop without sending a
// signal (there is no process yet), so delivery falls to attachRecordProcess.
// If it drops the stop, the operator has accepted a stop it never performed —
// trading a loud false 409 for a silent no-op (D-501 R4).
func TestStopRequestedBeforeSpawnIsDeliveredOnAttach(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	const jobID = "job-pre-spawn-delivery"
	rt.registerRecordJob(jobID)
	defer rt.unregisterRecordJob(jobID)

	state, alreadyStopping := rt.beginRecordStop(jobID)
	if state == nil {
		t.Fatalf("beginRecordStop found no registration before spawn")
	}
	if alreadyStopping {
		t.Fatalf("first stop reported alreadyStopping on a fresh registration")
	}
	if process, claimed := state.claimStopSignal(); claimed || process != nil {
		t.Fatalf("pre-spawn claim = (%v, %v), want (nil, false)", process, claimed)
	}

	// sleep outlives any plausible SIGTERM delivery: attach signals before it
	// returns, so a live sleep here means the stop was dropped, not delayed.
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	rt.attachRecordProcess(jobID, cmd.Process, make(chan struct{}), newRecordOutputActivity())

	// Exactly one signal: attach must have consumed the latch, so nothing may
	// claim again. Asserted before markRecordProcessExited — a closed done makes
	// claimStopSignal decline for an unrelated reason, which would mask a
	// delivery that bypassed the latch entirely and let a concurrent stop
	// SIGTERM the recorder a second time.
	if process, claimed := state.claimStopSignal(); claimed || process != nil {
		t.Fatalf("claim after delivery = (%v, %v), want (nil, false) — attach delivered the stop without consuming the signalled latch", process, claimed)
	}

	waitErr := cmd.Wait()
	rt.markRecordProcessExited(jobID)
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("sleep exit = %v, want an ExitError from SIGTERM", waitErr)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("sleep exit = %v, want termination by SIGTERM", waitErr)
	}
}

// TestRecordJobRegistrationOutlivesRecordProcess covers the mirror window at
// the far end of the stage: the recorder has exited, but the store stays in
// record/running until runRecordJob has promoted the bundle, retried Talk
// delivery and queued the build. The registration must outlive the subprocess
// and disappear only when the bracket closes. It also exercises the done
// double-close — markRecordProcessExited and unregisterRecordJob both close it.
func TestRecordJobRegistrationOutlivesRecordProcess(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	const jobID = "job-window-2"
	state := rt.registerRecordJob(jobID)
	if state == nil {
		t.Fatalf("registerRecordJob returned nil")
	}

	rt.markRecordProcessExited(jobID)
	select {
	case <-state.done:
	default:
		t.Fatalf("markRecordProcessExited did not close done")
	}
	found, _ := rt.beginRecordStop(jobID)
	if found == nil {
		t.Fatalf("registration vanished when the record process exited, reopening the D-501 window")
	}

	rt.unregisterRecordJob(jobID)
	if gone, _ := rt.beginRecordStop(jobID); gone != nil {
		t.Fatalf("registration survived unregisterRecordJob")
	}
}

func TestStopJobWaitsOutSlowFinalizationWithoutHardKill(t *testing.T) {
	rt, cleanup, _, startedPath := newCLITestRuntime(t)
	defer cleanup()
	// Shrink the ack grace far below the simulated compose time: the stop
	// must still succeed because the recorder acknowledged the SIGTERM
	// before going quiet to finalize.
	rt.recordStopAckGrace = 200 * time.Millisecond
	t.Setenv("FAKE_RECORD_WAIT_FOR_SIGNAL", "1")
	t.Setenv("FAKE_RECORD_FINALIZE_DELAY", "1")

	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/slow-finalize"}`))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp createJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	waitForFile(t, startedPath)
	waitForRecordState(t, rt.store, resp.ID, "running")

	stopReq := httptest.NewRequest(http.MethodPost, "/jobs/"+resp.ID+"/stop", nil)
	stopRec := httptest.NewRecorder()
	rt.jobDetailHandler(stopRec, stopReq)
	if stopRec.Code != http.StatusAccepted {
		t.Fatalf("stop status = %d, want %d body=%s", stopRec.Code, http.StatusAccepted, stopRec.Body.String())
	}

	job := waitForJobState(t, rt.store, resp.ID, "succeeded")
	if job.StopReason == nil || *job.StopReason != "operator_requested" {
		t.Fatalf("expected operator_requested stop reason, got %#v", job.StopReason)
	}
	if job.RecordExitCode == nil || *job.RecordExitCode != 0 {
		t.Fatalf("expected record exit code 0 (no hard kill), got %#v", job.RecordExitCode)
	}
	if job.RecordStopDetail == nil || *job.RecordStopDetail != "stop requested" {
		t.Fatalf("expected stop detail from stopping marker, got %#v", job.RecordStopDetail)
	}
}

func TestStopJobHardKillsRecorderThatIgnoresSigterm(t *testing.T) {
	rt, cleanup, _, startedPath := newCLITestRuntime(t)
	defer cleanup()
	rt.recordStopAckGrace = 200 * time.Millisecond
	t.Setenv("FAKE_RECORD_IGNORE_TERM", "1")

	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/wedged"}`))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp createJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	waitForFile(t, startedPath)
	waitForRecordState(t, rt.store, resp.ID, "running")

	stopReq := httptest.NewRequest(http.MethodPost, "/jobs/"+resp.ID+"/stop", nil)
	stopRec := httptest.NewRecorder()
	rt.jobDetailHandler(stopRec, stopReq)
	if stopRec.Code != http.StatusAccepted {
		t.Fatalf("stop status = %d, want %d body=%s", stopRec.Code, http.StatusAccepted, stopRec.Body.String())
	}

	// Without the stopping acknowledgement the recorder must be hard-killed
	// once it goes quiet past the ack grace; the kill surfaces as a failed
	// record stage (waitForJobState bounds how long that may take).
	job := waitForJobState(t, rt.store, resp.ID, "failed")
	if job.Stage != "done" {
		t.Fatalf("stage = %q, want done", job.Stage)
	}
	if job.Error == nil || !strings.Contains(*job.Error, "signal: killed") {
		t.Fatalf("expected hard-kill error, got %#v", job.Error)
	}
}

func TestShutdownWaitsForRecordFinalizationBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt, cleanup, _, startedPath := newCLITestRuntimeWithContext(t, ctx)
	defer cleanup()
	t.Setenv("FAKE_RECORD_WAIT_FOR_SIGNAL", "1")
	t.Setenv("FAKE_RECORD_FINALIZE_DELAY", "1")

	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/shutdown-me"}`))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp createJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	waitForFile(t, startedPath)
	waitForRecordState(t, rt.store, resp.ID, "running")

	// Operator shutdown: SIGTERM the recorder, then wait for the slow
	// finalization to complete instead of abandoning it.
	cancel()
	if !rt.WaitForRecordJobs(5 * time.Second) {
		t.Fatal("WaitForRecordJobs timed out; record job abandoned on shutdown")
	}

	job, err := rt.store.GetJob(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("GetJob(%s) error = %v", resp.ID, err)
	}
	if job.RecordFinishedAt == nil {
		t.Fatalf("expected record stage to finish during shutdown, got job=%#v", job)
	}
	recordingPath := filepath.Join(canonicalRunPath(rt.cfg.WorkRoot, resp.ID), "recording.mkv")
	if _, err := os.Stat(recordingPath); err != nil {
		t.Fatalf("expected promoted recording at %s: %v", recordingPath, err)
	}
}

func TestStopJobReturnsNotFoundAndConflict(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	missingReq := httptest.NewRequest(http.MethodPost, "/jobs/missing/stop", nil)
	missingRec := httptest.NewRecorder()
	rt.jobDetailHandler(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing stop status = %d, want %d body=%s", missingRec.Code, http.StatusNotFound, missingRec.Body.String())
	}

	createReq := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/call"}`))
	createRec := httptest.NewRecorder()
	rt.jobsHandler(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d body=%s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var resp createJobResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	_ = waitForJobState(t, rt.store, resp.ID, "succeeded")

	stopReq := httptest.NewRequest(http.MethodPost, "/jobs/"+resp.ID+"/stop", nil)
	stopRec := httptest.NewRecorder()
	rt.jobDetailHandler(stopRec, stopReq)
	if stopRec.Code != http.StatusConflict {
		t.Fatalf("stop status = %d, want %d body=%s", stopRec.Code, http.StatusConflict, stopRec.Body.String())
	}
}

func TestRerunFailedJobCreatesSecondAttemptAndPreservesFirst(t *testing.T) {
	rt, cleanup, logPath, _ := newCLITestRuntime(t)
	defer cleanup()

	secondBuildStarted := make(chan buildTask, 1)
	releaseSecondBuild := make(chan struct{})
	buildCalls := 0
	rt.buildJobFn = func(ctx context.Context, task buildTask) (string, error) {
		buildCalls++
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if buildCalls == 1 {
			if err := os.MkdirAll(meetingPath, 0o755); err != nil {
				return meetingPath, err
			}
			manifest := `{
  "kind": "meeting",
  "version": "cassini.meeting.v1",
  "state": "failed",
  "stage": "build",
  "error": "transcriber exploded"
}`
			if err := os.WriteFile(filepath.Join(meetingPath, "cassini.json"), []byte(manifest), 0o644); err != nil {
				return meetingPath, err
			}
			return meetingPath, errors.New("exit status 1")
		}
		select {
		case secondBuildStarted <- task:
		default:
		}
		<-releaseSecondBuild
		if err := writeReadyMeetingBundleFixture(meetingPath, task.ArtifactRunPath); err != nil {
			return meetingPath, err
		}
		return meetingPath, nil
	}

	createReq := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/rerun-me"}`))
	createRec := httptest.NewRecorder()
	rt.jobsHandler(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d body=%s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var createResp createJobResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	failedJob := waitForJobState(t, rt.store, createResp.ID, "failed")
	if failedJob.CurrentAttemptNumber != 1 {
		t.Fatalf("failed job current_attempt_number = %d, want 1", failedJob.CurrentAttemptNumber)
	}
	if failedJob.ArtifactRunPath == nil || !strings.Contains(*failedJob.ArtifactRunPath, filepath.Join("current", createResp.ID+".run")) {
		t.Fatalf("expected canonical run path to survive failed build, got %#v", failedJob.ArtifactRunPath)
	}
	failedAttempts, err := rt.store.ListJobAttempts(context.Background(), createResp.ID)
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(failedAttempts) != 1 {
		t.Fatalf("expected 1 failed attempt, got %d", len(failedAttempts))
	}
	firstAttempt := failedAttempts[0]
	if firstAttempt.State != "failed" || firstAttempt.AttemptNumber != 1 {
		t.Fatalf("unexpected first failed attempt = %#v", firstAttempt)
	}
	if _, err := os.Stat(attemptRunPath(rt.cfg.WorkRoot, createResp.ID, 2)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no rerun attempt-local run bundle before rerun, err=%v", err)
	}

	rerunReq := httptest.NewRequest(http.MethodPost, "/jobs/"+createResp.ID+"/rerun", nil)
	rerunRec := httptest.NewRecorder()
	rt.jobDetailHandler(rerunRec, rerunReq)
	if rerunRec.Code != http.StatusAccepted {
		t.Fatalf("rerun status = %d, want %d body=%s", rerunRec.Code, http.StatusAccepted, rerunRec.Body.String())
	}
	var rerunResp rerunJobResponse
	if err := json.Unmarshal(rerunRec.Body.Bytes(), &rerunResp); err != nil {
		t.Fatalf("decode rerun response: %v", err)
	}
	if rerunResp.AttemptNumber != 2 {
		t.Fatalf("rerun attempt_number = %d, want 2", rerunResp.AttemptNumber)
	}

	var rerunBuildTask buildTask
	select {
	case rerunBuildTask = <-secondBuildStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for rerun build to start")
	}
	if rerunBuildTask.AttemptNumber != 2 {
		t.Fatalf("rerun build attempt_number = %d, want 2", rerunBuildTask.AttemptNumber)
	}
	if rerunBuildTask.ArtifactRunPath != *failedJob.ArtifactRunPath {
		t.Fatalf("rerun build run path = %q, want %q", rerunBuildTask.ArtifactRunPath, *failedJob.ArtifactRunPath)
	}

	jobWhileRerunning := waitForJobStageState(t, rt.store, createResp.ID, "build", "running")
	if jobWhileRerunning.CurrentAttemptNumber != 2 {
		t.Fatalf("rerun job current_attempt_number = %d, want 2", jobWhileRerunning.CurrentAttemptNumber)
	}
	if jobWhileRerunning.RerunCount != 1 {
		t.Fatalf("rerun job rerun_count = %d, want 1", jobWhileRerunning.RerunCount)
	}
	if jobWhileRerunning.ArtifactRunPath == nil || *jobWhileRerunning.ArtifactRunPath != *failedJob.ArtifactRunPath {
		t.Fatalf("expected canonical run path to be preserved during rerun, got %#v", jobWhileRerunning.ArtifactRunPath)
	}
	if jobWhileRerunning.RecordQueuedAt == nil || failedJob.RecordQueuedAt == nil || *jobWhileRerunning.RecordQueuedAt != *failedJob.RecordQueuedAt {
		t.Fatalf("expected record_queued_at to be preserved across rerun, job=%#v failed=%#v", jobWhileRerunning, failedJob)
	}
	if jobWhileRerunning.RecordStartedAt == nil || failedJob.RecordStartedAt == nil || *jobWhileRerunning.RecordStartedAt != *failedJob.RecordStartedAt {
		t.Fatalf("expected record_started_at to be preserved across rerun, job=%#v failed=%#v", jobWhileRerunning, failedJob)
	}
	if jobWhileRerunning.RecordFinishedAt == nil || failedJob.RecordFinishedAt == nil || *jobWhileRerunning.RecordFinishedAt != *failedJob.RecordFinishedAt {
		t.Fatalf("expected record_finished_at to be preserved across rerun, job=%#v failed=%#v", jobWhileRerunning, failedJob)
	}
	if jobWhileRerunning.BuildQueuedAt == nil || jobWhileRerunning.BuildStartedAt == nil {
		t.Fatalf("expected fresh build timestamps during rerun, got %#v", jobWhileRerunning)
	}
	if jobWhileRerunning.PublishQueuedAt != nil || jobWhileRerunning.PublishStartedAt != nil || jobWhileRerunning.PublishFinishedAt != nil {
		t.Fatalf("did not expect publish timestamps before rerun build completes, got %#v", jobWhileRerunning)
	}

	attemptsWhileRerunning, err := rt.store.ListJobAttempts(context.Background(), createResp.ID)
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(attemptsWhileRerunning) != 2 {
		t.Fatalf("expected 2 attempts during rerun, got %d", len(attemptsWhileRerunning))
	}
	rerunAttempt := attemptsWhileRerunning[0]
	if rerunAttempt.AttemptNumber != 2 || rerunAttempt.TriggerKind != "rerun" || rerunAttempt.Stage != "build" || rerunAttempt.State != "running" {
		t.Fatalf("unexpected rerun attempt while running = %#v", rerunAttempt)
	}
	if rerunAttempt.ArtifactRunPath == nil || *rerunAttempt.ArtifactRunPath != *failedJob.ArtifactRunPath {
		t.Fatalf("expected canonical run source on rerun attempt, got %#v", rerunAttempt.ArtifactRunPath)
	}
	if rerunAttempt.RecordQueuedAt != nil || rerunAttempt.RecordStartedAt != nil || rerunAttempt.RecordFinishedAt != nil || rerunAttempt.RecordLogPath != nil {
		t.Fatalf("did not expect record fields on rerun attempt, got %#v", rerunAttempt)
	}
	if _, err := os.Stat(attemptRunPath(rt.cfg.WorkRoot, createResp.ID, 2)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected rerun to avoid creating attempt-local run bundle, err=%v", err)
	}

	close(releaseSecondBuild)

	succeededJob := waitForJobState(t, rt.store, createResp.ID, "succeeded")
	if succeededJob.CurrentAttemptNumber != 2 {
		t.Fatalf("succeeded job current_attempt_number = %d, want 2", succeededJob.CurrentAttemptNumber)
	}
	if succeededJob.RerunCount != 1 {
		t.Fatalf("succeeded job rerun_count = %d, want 1", succeededJob.RerunCount)
	}
	if succeededJob.ArtifactRunPath == nil || *succeededJob.ArtifactRunPath != *failedJob.ArtifactRunPath {
		t.Fatalf("expected canonical run path after rerun success, got %#v", succeededJob.ArtifactRunPath)
	}
	if succeededJob.ArtifactMeetingPath == nil || !strings.Contains(*succeededJob.ArtifactMeetingPath, filepath.Join("current", createResp.ID+".meeting")) {
		t.Fatalf("expected canonical meeting path after rerun success, got %#v", succeededJob.ArtifactMeetingPath)
	}
	if succeededJob.RecordFinishedAt == nil || failedJob.RecordFinishedAt == nil || *succeededJob.RecordFinishedAt != *failedJob.RecordFinishedAt {
		t.Fatalf("expected record timestamps to stay preserved after rerun success, job=%#v failed=%#v", succeededJob, failedJob)
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), createResp.ID)
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts after rerun, got %d", len(attempts))
	}
	if attempts[0].AttemptNumber != 2 || attempts[0].State != "succeeded" {
		t.Fatalf("unexpected rerun attempt = %#v", attempts[0])
	}
	if attempts[1].AttemptNumber != 1 || attempts[1].State != "failed" {
		t.Fatalf("unexpected preserved first attempt = %#v", attempts[1])
	}
	if attempts[0].RecordLogPath != nil {
		t.Fatalf("did not expect rerun record log path, got %#v", attempts[0].RecordLogPath)
	}
	if attempts[1].ArtifactMeetingPath == nil || attempts[0].ArtifactMeetingPath == nil || *attempts[1].ArtifactMeetingPath == *attempts[0].ArtifactMeetingPath {
		t.Fatalf("expected distinct meeting artifact paths across attempts, got %#v and %#v", attempts[1].ArtifactMeetingPath, attempts[0].ArtifactMeetingPath)
	}
	if buildCalls != 2 {
		t.Fatalf("build call count = %d, want 2", buildCalls)
	}
	if _, err := os.Stat(attemptRunPath(rt.cfg.WorkRoot, createResp.ID, 2)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected rerun to avoid creating attempt-local run bundle after success, err=%v", err)
	}
	logText := readFileString(t, logPath)
	if got := strings.Count(logText, "--call https://example.test/rerun-me"); got != 1 {
		t.Fatalf("expected exactly one record invocation across initial run + rerun, got %d log=%s", got, logText)
	}
}

func TestRerunFromPublishFailureRebuildsInsteadOfPublishOnly(t *testing.T) {
	rt, cleanup, logPath, _ := newCLITestRuntime(t)
	defer cleanup()

	buildCalls := 0
	publishCalls := 0
	rt.buildJobFn = func(ctx context.Context, task buildTask) (string, error) {
		buildCalls++
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if err := writeReadyMeetingBundleFixture(meetingPath, task.ArtifactRunPath); err != nil {
			return meetingPath, err
		}
		return meetingPath, nil
	}
	rt.publishJobFn = func(ctx context.Context, task publishTask) (string, error) {
		publishCalls++
		sitePath := attemptSitePath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if publishCalls == 1 {
			if err := os.MkdirAll(sitePath, 0o755); err != nil {
				return sitePath, err
			}
			manifest := `{
  "kind": "site",
  "version": "cassini.site.v1",
  "state": "failed",
  "stage": "publish",
  "error": "exporter exploded"
}`
			if err := os.WriteFile(filepath.Join(sitePath, "cassini.json"), []byte(manifest), 0o644); err != nil {
				return sitePath, err
			}
			return sitePath, errors.New("exit status 1")
		}
		if err := writeReadySiteBundleFixture(sitePath, currentRoot(rt.cfg.WorkRoot)); err != nil {
			return sitePath, err
		}
		return sitePath, nil
	}

	createReq := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/publish-rerun"}`))
	createRec := httptest.NewRecorder()
	rt.jobsHandler(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d body=%s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var createResp createJobResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	failedJob := waitForJobState(t, rt.store, createResp.ID, "failed")
	if failedJob.ArtifactRunPath == nil || !strings.Contains(*failedJob.ArtifactRunPath, filepath.Join("current", createResp.ID+".run")) {
		t.Fatalf("expected canonical run path on publish failure, got %#v", failedJob.ArtifactRunPath)
	}
	if failedJob.ArtifactMeetingPath == nil || !strings.Contains(*failedJob.ArtifactMeetingPath, filepath.Join("current", createResp.ID+".meeting")) {
		t.Fatalf("expected canonical meeting path on publish failure, got %#v", failedJob.ArtifactMeetingPath)
	}
	if buildCalls != 1 || publishCalls != 1 {
		t.Fatalf("expected one build and one publish before rerun, got build=%d publish=%d", buildCalls, publishCalls)
	}

	rerunReq := httptest.NewRequest(http.MethodPost, "/jobs/"+createResp.ID+"/rerun", nil)
	rerunRec := httptest.NewRecorder()
	rt.jobDetailHandler(rerunRec, rerunReq)
	if rerunRec.Code != http.StatusAccepted {
		t.Fatalf("rerun status = %d, want %d body=%s", rerunRec.Code, http.StatusAccepted, rerunRec.Body.String())
	}
	_ = waitForJobState(t, rt.store, createResp.ID, "succeeded")

	if buildCalls != 2 || publishCalls != 2 {
		t.Fatalf("expected rerun to redo build and publish, got build=%d publish=%d", buildCalls, publishCalls)
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), createResp.ID)
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts after rerun, got %d", len(attempts))
	}
	if attempts[0].AttemptNumber != 2 || attempts[0].TriggerKind != "rerun" || attempts[0].State != "succeeded" {
		t.Fatalf("unexpected rerun attempt = %#v", attempts[0])
	}
	if attempts[0].RecordQueuedAt != nil || attempts[0].RecordStartedAt != nil || attempts[0].RecordFinishedAt != nil || attempts[0].RecordLogPath != nil {
		t.Fatalf("did not expect record fields on rerun attempt, got %#v", attempts[0])
	}
	if attempts[0].BuildQueuedAt == nil || attempts[0].BuildStartedAt == nil || attempts[0].BuildFinishedAt == nil {
		t.Fatalf("expected build timestamps on rerun attempt, got %#v", attempts[0])
	}
	if attempts[0].PublishQueuedAt == nil || attempts[0].PublishStartedAt == nil || attempts[0].PublishFinishedAt == nil {
		t.Fatalf("expected publish timestamps on rerun attempt, got %#v", attempts[0])
	}
	if attempts[0].ArtifactSitePath == nil || !strings.Contains(*attempts[0].ArtifactSitePath, filepath.Join("runs", createResp.ID+"--attempt-002.site")) {
		t.Fatalf("expected rerun attempt-local site path, got %#v", attempts[0].ArtifactSitePath)
	}
	logText := readFileString(t, logPath)
	if got := strings.Count(logText, "--call https://example.test/publish-rerun"); got != 1 {
		t.Fatalf("expected exactly one record invocation across publish rerun flow, got %d log=%s", got, logText)
	}
}

func TestRerunSucceededJobCreatesFreshDownstreamAttempt(t *testing.T) {
	rt, cleanup, logPath, _ := newCLITestRuntime(t)
	defer cleanup()

	secondBuildStarted := make(chan buildTask, 1)
	releaseSecondBuild := make(chan struct{})
	buildCalls := 0
	rt.buildJobFn = func(ctx context.Context, task buildTask) (string, error) {
		buildCalls++
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if buildCalls == 2 {
			select {
			case secondBuildStarted <- task:
			default:
			}
			<-releaseSecondBuild
		}
		if err := writeReadyMeetingBundleFixture(meetingPath, task.ArtifactRunPath); err != nil {
			return meetingPath, err
		}
		return meetingPath, nil
	}

	createReq := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/rerun-succeeded"}`))
	createRec := httptest.NewRecorder()
	rt.jobsHandler(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d body=%s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var createResp createJobResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	initialJob := waitForJobState(t, rt.store, createResp.ID, "succeeded")
	if initialJob.ArtifactRunPath == nil || !strings.Contains(*initialJob.ArtifactRunPath, filepath.Join("current", createResp.ID+".run")) {
		t.Fatalf("expected canonical run path on initial success, got %#v", initialJob.ArtifactRunPath)
	}
	if initialJob.ArtifactMeetingPath == nil || !strings.Contains(*initialJob.ArtifactMeetingPath, filepath.Join("current", createResp.ID+".meeting")) {
		t.Fatalf("expected canonical meeting path on initial success, got %#v", initialJob.ArtifactMeetingPath)
	}
	if initialJob.ArtifactSitePath == nil {
		t.Fatalf("expected site artifact on initial success, got %#v", initialJob.ArtifactSitePath)
	}

	rerunReq := httptest.NewRequest(http.MethodPost, "/jobs/"+createResp.ID+"/rerun", nil)
	rerunRec := httptest.NewRecorder()
	rt.jobDetailHandler(rerunRec, rerunReq)
	if rerunRec.Code != http.StatusAccepted {
		t.Fatalf("rerun status = %d, want %d body=%s", rerunRec.Code, http.StatusAccepted, rerunRec.Body.String())
	}
	var rerunResp rerunJobResponse
	if err := json.Unmarshal(rerunRec.Body.Bytes(), &rerunResp); err != nil {
		t.Fatalf("decode rerun response: %v", err)
	}
	if rerunResp.AttemptNumber != 2 {
		t.Fatalf("rerun attempt_number = %d, want 2", rerunResp.AttemptNumber)
	}

	var rerunBuildTask buildTask
	select {
	case rerunBuildTask = <-secondBuildStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for successful-job rerun build to start")
	}
	if rerunBuildTask.AttemptNumber != 2 {
		t.Fatalf("rerun build attempt_number = %d, want 2", rerunBuildTask.AttemptNumber)
	}
	if rerunBuildTask.ArtifactRunPath != *initialJob.ArtifactRunPath {
		t.Fatalf("rerun build run path = %q, want %q", rerunBuildTask.ArtifactRunPath, *initialJob.ArtifactRunPath)
	}

	jobWhileRerunning := waitForJobStageState(t, rt.store, createResp.ID, "build", "running")
	if jobWhileRerunning.CurrentAttemptNumber != 2 {
		t.Fatalf("rerun job current_attempt_number = %d, want 2", jobWhileRerunning.CurrentAttemptNumber)
	}
	if jobWhileRerunning.RerunCount != 1 {
		t.Fatalf("rerun job rerun_count = %d, want 1", jobWhileRerunning.RerunCount)
	}
	if jobWhileRerunning.ArtifactRunPath == nil || *jobWhileRerunning.ArtifactRunPath != *initialJob.ArtifactRunPath {
		t.Fatalf("expected canonical run path to be preserved during successful rerun, got %#v", jobWhileRerunning.ArtifactRunPath)
	}
	if jobWhileRerunning.ArtifactMeetingPath == nil || *jobWhileRerunning.ArtifactMeetingPath != *initialJob.ArtifactMeetingPath {
		t.Fatalf("expected canonical meeting path to remain visible during successful rerun, got %#v", jobWhileRerunning.ArtifactMeetingPath)
	}
	if jobWhileRerunning.ArtifactSitePath == nil || *jobWhileRerunning.ArtifactSitePath != *initialJob.ArtifactSitePath {
		t.Fatalf("expected site path to remain visible during successful rerun, got %#v", jobWhileRerunning.ArtifactSitePath)
	}
	if jobWhileRerunning.RecordFinishedAt == nil || initialJob.RecordFinishedAt == nil || *jobWhileRerunning.RecordFinishedAt != *initialJob.RecordFinishedAt {
		t.Fatalf("expected record timestamps to be preserved across successful rerun, job=%#v initial=%#v", jobWhileRerunning, initialJob)
	}

	attemptsWhileRerunning, err := rt.store.ListJobAttempts(context.Background(), createResp.ID)
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(attemptsWhileRerunning) != 2 {
		t.Fatalf("expected 2 attempts during successful rerun, got %d", len(attemptsWhileRerunning))
	}
	rerunAttempt := attemptsWhileRerunning[0]
	if rerunAttempt.AttemptNumber != 2 || rerunAttempt.TriggerKind != "rerun" || rerunAttempt.Stage != "build" || rerunAttempt.State != "running" {
		t.Fatalf("unexpected rerun attempt while running = %#v", rerunAttempt)
	}
	if rerunAttempt.ArtifactRunPath == nil || *rerunAttempt.ArtifactRunPath != *initialJob.ArtifactRunPath {
		t.Fatalf("expected canonical run source on successful rerun attempt, got %#v", rerunAttempt.ArtifactRunPath)
	}
	if rerunAttempt.RecordQueuedAt != nil || rerunAttempt.RecordStartedAt != nil || rerunAttempt.RecordFinishedAt != nil || rerunAttempt.RecordLogPath != nil {
		t.Fatalf("did not expect record fields on successful rerun attempt, got %#v", rerunAttempt)
	}
	if _, err := os.Stat(attemptRunPath(rt.cfg.WorkRoot, createResp.ID, 2)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected successful rerun to avoid creating attempt-local run bundle, err=%v", err)
	}

	close(releaseSecondBuild)

	succeededJob := waitForJobState(t, rt.store, createResp.ID, "succeeded")
	if succeededJob.CurrentAttemptNumber != 2 {
		t.Fatalf("succeeded job current_attempt_number = %d, want 2", succeededJob.CurrentAttemptNumber)
	}
	if succeededJob.RerunCount != 1 {
		t.Fatalf("succeeded job rerun_count = %d, want 1", succeededJob.RerunCount)
	}
	if succeededJob.ArtifactRunPath == nil || *succeededJob.ArtifactRunPath != *initialJob.ArtifactRunPath {
		t.Fatalf("expected canonical run path after successful rerun, got %#v", succeededJob.ArtifactRunPath)
	}
	liveSiteManifest, ok, err := LoadSiteBundleManifest(rt.cfg.SiteRoot)
	if err != nil {
		t.Fatalf("LoadSiteBundleManifest() error = %v", err)
	}
	if !ok {
		t.Fatalf("expected live site bundle manifest after successful rerun")
	}
	if liveSiteManifest.PublishedByJobID != createResp.ID || liveSiteManifest.PublishedByAttemptNumber != 2 {
		t.Fatalf("unexpected live site lineage after successful rerun = %#v", liveSiteManifest)
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), createResp.ID)
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts after successful rerun, got %d", len(attempts))
	}
	if attempts[0].AttemptNumber != 2 || attempts[0].TriggerKind != "rerun" || attempts[0].State != "succeeded" {
		t.Fatalf("unexpected rerun attempt = %#v", attempts[0])
	}
	if attempts[1].AttemptNumber != 1 || attempts[1].State != "succeeded" {
		t.Fatalf("unexpected preserved first attempt = %#v", attempts[1])
	}
	if attempts[0].RecordLogPath != nil {
		t.Fatalf("did not expect record log path on successful rerun attempt, got %#v", attempts[0].RecordLogPath)
	}
	if attempts[0].ArtifactSitePath == nil || !strings.Contains(*attempts[0].ArtifactSitePath, filepath.Join("runs", createResp.ID+"--attempt-002.site")) {
		t.Fatalf("expected successful rerun attempt-local site path, got %#v", attempts[0].ArtifactSitePath)
	}
	if attempts[1].ArtifactSitePath == nil || !strings.Contains(*attempts[1].ArtifactSitePath, filepath.Join("runs", createResp.ID+"--attempt-001.site")) {
		t.Fatalf("expected preserved first attempt-local site path, got %#v", attempts[1].ArtifactSitePath)
	}
	if buildCalls != 2 {
		t.Fatalf("build call count = %d, want 2", buildCalls)
	}
	logText := readFileString(t, logPath)
	if got := strings.Count(logText, "--call https://example.test/rerun-succeeded"); got != 1 {
		t.Fatalf("expected exactly one record invocation across successful rerun flow, got %d log=%s", got, logText)
	}
}

func TestFailedRerunPreservesPreviouslyDeployedSite(t *testing.T) {
	rt, cleanup, logPath, _ := newCLITestRuntime(t)
	defer cleanup()

	publishCalls := 0
	rt.publishJobFn = func(ctx context.Context, task publishTask) (string, error) {
		publishCalls++
		sitePath := attemptSitePath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if publishCalls == 1 {
			if err := writeReadySiteBundleFixture(sitePath, currentRoot(rt.cfg.WorkRoot)); err != nil {
				return sitePath, err
			}
			return sitePath, nil
		}
		if err := os.MkdirAll(sitePath, 0o755); err != nil {
			return sitePath, err
		}
		manifest := `{
  "kind": "site",
  "version": "cassini.site.v1",
  "state": "failed",
  "stage": "publish",
  "error": "exporter exploded"
}`
		if err := os.WriteFile(filepath.Join(sitePath, "cassini.json"), []byte(manifest), 0o644); err != nil {
			return sitePath, err
		}
		return sitePath, errors.New("exit status 1")
	}

	createReq := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/rerun-preserve-site"}`))
	createRec := httptest.NewRecorder()
	rt.jobsHandler(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d body=%s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var createResp createJobResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	initialJob := waitForJobState(t, rt.store, createResp.ID, "succeeded")
	if initialJob.ArtifactSitePath == nil || *initialJob.ArtifactSitePath != rt.cfg.SiteRoot {
		t.Fatalf("expected shared live site path on initial success, got %#v", initialJob.ArtifactSitePath)
	}
	initialLiveManifest, ok, err := LoadSiteBundleManifest(rt.cfg.SiteRoot)
	if err != nil {
		t.Fatalf("LoadSiteBundleManifest(initial) error = %v", err)
	}
	if !ok {
		t.Fatalf("expected initial live site manifest")
	}
	if initialLiveManifest.PublishedByJobID != createResp.ID || initialLiveManifest.PublishedByAttemptNumber != 1 {
		t.Fatalf("unexpected initial live site lineage = %#v", initialLiveManifest)
	}

	rerunReq := httptest.NewRequest(http.MethodPost, "/jobs/"+createResp.ID+"/rerun", nil)
	rerunRec := httptest.NewRecorder()
	rt.jobDetailHandler(rerunRec, rerunReq)
	if rerunRec.Code != http.StatusAccepted {
		t.Fatalf("rerun status = %d, want %d body=%s", rerunRec.Code, http.StatusAccepted, rerunRec.Body.String())
	}

	failedJob := waitForJobState(t, rt.store, createResp.ID, "failed")
	if failedJob.ArtifactSitePath == nil || *failedJob.ArtifactSitePath != rt.cfg.SiteRoot {
		t.Fatalf("expected previous live site path to remain visible after failed rerun, got %#v", failedJob.ArtifactSitePath)
	}
	liveManifest, ok, err := LoadSiteBundleManifest(rt.cfg.SiteRoot)
	if err != nil {
		t.Fatalf("LoadSiteBundleManifest(after failed rerun) error = %v", err)
	}
	if !ok {
		t.Fatalf("expected live site manifest after failed rerun")
	}
	if liveManifest.PublishedByJobID != createResp.ID || liveManifest.PublishedByAttemptNumber != 1 {
		t.Fatalf("expected live site lineage to remain on attempt 1, got %#v", liveManifest)
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), createResp.ID)
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(attempts))
	}
	if attempts[0].AttemptNumber != 2 || attempts[0].State != "failed" {
		t.Fatalf("unexpected latest failed rerun attempt = %#v", attempts[0])
	}
	if attempts[0].ArtifactSitePath == nil || !strings.Contains(*attempts[0].ArtifactSitePath, filepath.Join("runs", createResp.ID+"--attempt-002.site")) {
		t.Fatalf("expected failed rerun to retain attempt-local site path, got %#v", attempts[0].ArtifactSitePath)
	}
	if attempts[1].ArtifactSitePath == nil || !strings.Contains(*attempts[1].ArtifactSitePath, filepath.Join("runs", createResp.ID+"--attempt-001.site")) {
		t.Fatalf("expected initial successful attempt-local site path, got %#v", attempts[1].ArtifactSitePath)
	}
	if publishCalls != 2 {
		t.Fatalf("publish call count = %d, want 2", publishCalls)
	}
	logText := readFileString(t, logPath)
	if got := strings.Count(logText, "--call https://example.test/rerun-preserve-site"); got != 1 {
		t.Fatalf("expected exactly one record invocation across failed rerun flow, got %d log=%s", got, logText)
	}
}

func TestRerunRejectsUnknownAndActiveJobs(t *testing.T) {
	rt, cleanup, _, startedPath := newCLITestRuntime(t)
	defer cleanup()
	t.Setenv("FAKE_RECORD_WAIT_FOR_SIGNAL", "1")

	missingReq := httptest.NewRequest(http.MethodPost, "/jobs/missing/rerun", nil)
	missingRec := httptest.NewRecorder()
	rt.jobDetailHandler(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing rerun status = %d, want %d body=%s", missingRec.Code, http.StatusNotFound, missingRec.Body.String())
	}

	createReq := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/running-rerun"}`))
	createRec := httptest.NewRecorder()
	rt.jobsHandler(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d body=%s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var resp createJobResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	waitForFile(t, startedPath)
	_ = waitForRecordState(t, rt.store, resp.ID, "running")

	rerunReq := httptest.NewRequest(http.MethodPost, "/jobs/"+resp.ID+"/rerun", nil)
	rerunRec := httptest.NewRecorder()
	rt.jobDetailHandler(rerunRec, rerunReq)
	if rerunRec.Code != http.StatusConflict {
		t.Fatalf("rerun status = %d, want %d body=%s", rerunRec.Code, http.StatusConflict, rerunRec.Body.String())
	}

	stopReq := httptest.NewRequest(http.MethodPost, "/jobs/"+resp.ID+"/stop", nil)
	stopRec := httptest.NewRecorder()
	rt.jobDetailHandler(stopRec, stopReq)
	if stopRec.Code != http.StatusAccepted {
		t.Fatalf("stop status = %d, want %d body=%s", stopRec.Code, http.StatusAccepted, stopRec.Body.String())
	}
	_ = waitForJobState(t, rt.store, resp.ID, "succeeded")
}

func TestRerunBlockedBuildCreatesFreshAttempt(t *testing.T) {
	tmp := t.TempDir()
	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rt := &Runtime{
		store:      store,
		logger:     log.New(ioDiscard{}, "", 0),
		buildQueue: make(chan buildTask, 1),
	}

	const jobID = "blocked-rerun"
	insertJob(t, store.db, jobID, nowUTCString())
	runPath := seedReadyRunBundle(t, filepath.Join(tmp, "jobs"), jobID)
	if err := store.MarkBuildQueued(context.Background(), jobID, runPath, runPath, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	task := buildTask{JobID: jobID, AttemptNumber: 1, ArtifactRunPath: runPath}
	if claimed, err := store.ClaimBuildRunning(context.Background(), task, nowUTCString()); err != nil || !claimed {
		t.Fatalf("ClaimBuildRunning() = %t, %v", claimed, err)
	}
	if blocked, err := store.MarkBuildBlocked(context.Background(), task, "resource governor: CUDA runtime unavailable", nowUTCString()); err != nil || !blocked {
		t.Fatalf("MarkBuildBlocked() = %t, %v", blocked, err)
	}

	req := httptest.NewRequest(http.MethodPost, "/jobs/"+jobID+"/rerun", nil)
	rec := httptest.NewRecorder()
	rt.handleRerunJob(rec, req, jobID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("rerun status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	job := mustGetJob(t, store, jobID)
	if job.Stage != "build" || job.State != "queued" || job.CurrentAttemptNumber != 2 || job.RerunCount != 1 || job.BuildDeferralCount != 0 || job.Error != nil {
		t.Fatalf("blocked rerun job = %#v", job)
	}
	select {
	case queued := <-rt.buildQueue:
		if queued.JobID != jobID || queued.AttemptNumber != 2 || queued.DeferralCount != 0 {
			t.Fatalf("rerun task = %#v", queued)
		}
	default:
		t.Fatal("rerun task was not queued")
	}
}

func TestRerunInterruptedJobWithReadyRunSucceeds(t *testing.T) {
	rt, cleanup, _, _ := newCLITestRuntime(t)
	defer cleanup()

	createReq := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/interrupted-rerun"}`))
	createRec := httptest.NewRecorder()
	rt.jobsHandler(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d body=%s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var createResp createJobResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	_ = waitForJobState(t, rt.store, createResp.ID, "succeeded")

	// Simulate the startup sweep after a crash mid-build: the job is frozen
	// at stage build, state interrupted, with a perfectly ready canonical
	// run bundle on disk (the empirical D-362 repro).
	interruptedAt := "2026-06-12T10:00:00Z"
	if _, err := rt.store.db.Exec(`
UPDATE jobs
SET stage = 'build', state = 'interrupted', interrupted_at = ?, completed_at = NULL,
    build_finished_at = NULL, publish_queued_at = NULL, publish_started_at = NULL, publish_finished_at = NULL
WHERE id = ?`, interruptedAt, createResp.ID); err != nil {
		t.Fatalf("mark job interrupted: %v", err)
	}
	if _, err := rt.store.db.Exec(`
UPDATE job_attempts
SET stage = 'build', state = 'interrupted', interrupted_at = ?, completed_at = NULL
WHERE job_id = ?`, interruptedAt, createResp.ID); err != nil {
		t.Fatalf("mark attempt interrupted: %v", err)
	}

	rerunReq := httptest.NewRequest(http.MethodPost, "/jobs/"+createResp.ID+"/rerun", nil)
	rerunRec := httptest.NewRecorder()
	rt.jobDetailHandler(rerunRec, rerunReq)
	if rerunRec.Code != http.StatusAccepted {
		t.Fatalf("rerun status = %d, want %d body=%s", rerunRec.Code, http.StatusAccepted, rerunRec.Body.String())
	}
	var rerunResp rerunJobResponse
	if err := json.Unmarshal(rerunRec.Body.Bytes(), &rerunResp); err != nil {
		t.Fatalf("decode rerun response: %v", err)
	}
	if rerunResp.AttemptNumber != 2 {
		t.Fatalf("rerun attempt_number = %d, want 2", rerunResp.AttemptNumber)
	}

	job := waitForJobState(t, rt.store, createResp.ID, "succeeded")
	if job.CurrentAttemptNumber != 2 || job.RerunCount != 1 {
		t.Fatalf("expected attempt 2 / rerun_count 1 after interrupted rerun, got %#v", job)
	}
	if job.InterruptedAt != nil {
		t.Fatalf("expected interrupted_at to be cleared after rerun, got %#v", job.InterruptedAt)
	}
}

func TestRerunRejectsInterruptedRecordJobWithoutCanonicalRun(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	// A job interrupted mid-recording has no canonical run to rebuild from:
	// the rerun gate lets interrupted jobs through, but the ready-run check
	// must still reject it.
	seedJobRow(t, rt.store.db, seededJobRow{ID: "interrupted-record", Stage: "record", State: "interrupted", CreatedAt: "2026-06-12T10:00:00Z"})

	rerunReq := httptest.NewRequest(http.MethodPost, "/jobs/interrupted-record/rerun", nil)
	rerunRec := httptest.NewRecorder()
	rt.jobDetailHandler(rerunRec, rerunReq)
	if rerunRec.Code != http.StatusConflict {
		t.Fatalf("rerun status = %d, want %d body=%s", rerunRec.Code, http.StatusConflict, rerunRec.Body.String())
	}
}

func TestRerunRejectsFailedRecordWithoutCanonicalRun(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rt.recordJobFn = func(ctx context.Context, job Job, req TriggerRequest) (recordResult, error) {
		runPath := attemptRunPath(rt.cfg.WorkRoot, job.ID, job.CurrentAttemptNumber)
		if err := os.MkdirAll(runPath, 0o755); err != nil {
			return recordResult{}, err
		}
		return recordResult{ArtifactRunPath: runPath, ExitCode: intPtr(1), StopDetail: "recorder exploded"}, errors.New("cassini record: exit status 1")
	}

	createReq := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/no-canonical-run"}`))
	createRec := httptest.NewRecorder()
	rt.jobsHandler(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d body=%s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var resp createJobResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	job := waitForJobState(t, rt.store, resp.ID, "failed")
	if job.ArtifactRunPath != nil {
		t.Fatalf("did not expect canonical run path after failed record, got %#v", job.ArtifactRunPath)
	}

	rerunReq := httptest.NewRequest(http.MethodPost, "/jobs/"+resp.ID+"/rerun", nil)
	rerunRec := httptest.NewRecorder()
	rt.jobDetailHandler(rerunRec, rerunReq)
	if rerunRec.Code != http.StatusConflict {
		t.Fatalf("rerun status = %d, want %d body=%s", rerunRec.Code, http.StatusConflict, rerunRec.Body.String())
	}
}

func TestCreateJobRejectsUnknownProvider(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=zoom", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/call"}`))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	jobs, err := rt.store.ListJobs(context.Background())
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no jobs after unknown provider, got %d", len(jobs))
	}
}

func TestCreateJobRejectsInvalidBody(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk"}`))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestCreateJobReturnsBusyWithoutCreatingRow(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	block := make(chan struct{})
	done := make(chan struct{})
	rt.recordJobFn = func(ctx context.Context, job Job, req TriggerRequest) (recordResult, error) {
		defer close(done)
		<-block
		runPath := attemptRunPath(rt.cfg.WorkRoot, job.ID, job.CurrentAttemptNumber)
		bundle, err := PrepareRunBundle(runPath, false)
		if err != nil {
			return recordResult{}, err
		}
		if err := os.WriteFile(bundle.RecordingPath, []byte("fake-mkv"), 0o644); err != nil {
			return recordResult{}, err
		}
		if err := FinalizeRunBundle(bundle, RunManifest{SourceMode: "talk", RecorderName: req.GuestName}); err != nil {
			return recordResult{}, err
		}
		return recordResult{ArtifactRunPath: bundle.RootDir, StopReason: "room_empty", ExitCode: intPtr(0), StopDetail: "room empty for 30s after remote participants left"}, nil
	}

	req1 := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/one"}`))
	rec1 := httptest.NewRecorder()
	rt.jobsHandler(rec1, req1)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, want %d body=%s", rec1.Code, http.StatusAccepted, rec1.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/two"}`))
	rec2 := httptest.NewRecorder()
	rt.jobsHandler(rec2, req2)
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, want %d body=%s", rec2.Code, http.StatusServiceUnavailable, rec2.Body.String())
	}

	jobs, err := rt.store.ListJobs(context.Background())
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected exactly one persisted job, got %d", len(jobs))
	}
	close(block)
	<-done
	_ = waitForJobState(t, rt.store, jobs[0].ID, "succeeded")
}

func TestRecordFailureRetainsAttemptRunWithoutCanonicalRun(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rt.recordJobFn = func(ctx context.Context, job Job, req TriggerRequest) (recordResult, error) {
		runPath := attemptRunPath(rt.cfg.WorkRoot, job.ID, job.CurrentAttemptNumber)
		bundle, err := PrepareRunBundle(runPath, false)
		if err != nil {
			return recordResult{}, err
		}
		if err := os.WriteFile(bundle.RecordingPath, []byte("partial-mkv"), 0o644); err != nil {
			return recordResult{}, err
		}
		if err := UpdateRunBundleStatus(bundle, bundleStateFailed, "record", "recorder exploded"); err != nil {
			return recordResult{}, err
		}
		return recordResult{ArtifactRunPath: bundle.RootDir, StopReason: "error", StopDetail: "recorder exploded"}, errors.New("recorder exploded")
	}

	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/record-fail"}`))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp createJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	job := waitForJobState(t, rt.store, resp.ID, "failed")
	if job.ArtifactRunPath != nil {
		t.Fatalf("did not expect canonical artifact_run_path on failed record job, got %#v", job.ArtifactRunPath)
	}
	if _, err := os.Stat(canonicalRunPath(rt.cfg.WorkRoot, resp.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no canonical run bundle after record failure, got err=%v", err)
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(attempts))
	}
	if attempts[0].ArtifactRunPath == nil || !strings.Contains(*attempts[0].ArtifactRunPath, filepath.Join("runs", resp.ID+"--attempt-001.run")) {
		t.Fatalf("expected retained failed attempt-local run path, got %#v", attempts[0].ArtifactRunPath)
	}
}

func TestBuildFailurePersistsLightweightErrorDetail(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rt.buildJobFn = func(ctx context.Context, task buildTask) (string, error) {
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if err := os.MkdirAll(meetingPath, 0o755); err != nil {
			return meetingPath, err
		}
		manifest := `{
  "kind": "meeting",
  "version": "cassini.meeting.v1",
  "state": "failed",
  "stage": "build",
  "error": "transcriber exploded"
}`
		if err := os.WriteFile(filepath.Join(meetingPath, "cassini.json"), []byte(manifest), 0o644); err != nil {
			return meetingPath, err
		}
		return meetingPath, errors.New("exit status 1")
	}

	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/fail"}`))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp createJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	job := waitForJobState(t, rt.store, resp.ID, "failed")
	if job.ArtifactRunPath == nil || !strings.Contains(*job.ArtifactRunPath, filepath.Join("current", resp.ID+".run")) {
		t.Fatalf("expected canonical run path to remain on failed build job, got %#v", job.ArtifactRunPath)
	}
	if job.ArtifactMeetingPath != nil {
		t.Fatalf("did not expect canonical meeting path on initial build failure, got %#v", job.ArtifactMeetingPath)
	}
	if job.Error == nil || *job.Error != "build stage build: transcriber exploded" {
		t.Fatalf("unexpected error detail: %#v", job.Error)
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(attempts) != 1 || attempts[0].ArtifactMeetingPath == nil {
		t.Fatalf("expected partial attempt-local meeting bundle on first attempt, got %#v", attempts)
	}
	if !strings.Contains(*attempts[0].ArtifactMeetingPath, filepath.Join("runs", resp.ID+"--attempt-001.meeting")) {
		t.Fatalf("expected attempt-local partial meeting path, got %#v", attempts[0].ArtifactMeetingPath)
	}
}

func TestPublishFailurePersistsLightweightErrorDetail(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rt.publishJobFn = func(ctx context.Context, task publishTask) (string, error) {
		sitePath := attemptSitePath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if err := os.MkdirAll(sitePath, 0o755); err != nil {
			return sitePath, err
		}
		manifest := `{
  "kind": "site",
  "version": "cassini.site.v1",
  "state": "failed",
  "stage": "publish",
  "error": "exporter exploded"
}`
		if err := os.WriteFile(filepath.Join(sitePath, "cassini.json"), []byte(manifest), 0o644); err != nil {
			return sitePath, err
		}
		return sitePath, errors.New("exit status 1")
	}

	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/publish-fail"}`))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp createJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	job := waitForJobState(t, rt.store, resp.ID, "failed")
	if job.ArtifactMeetingPath == nil || !strings.Contains(*job.ArtifactMeetingPath, filepath.Join("current", resp.ID+".meeting")) {
		t.Fatalf("expected canonical meeting path to remain on publish failure, got %#v", job.ArtifactMeetingPath)
	}
	if job.ArtifactSitePath != nil {
		t.Fatalf("did not expect shared artifact_site_path on initial publish failure, got %#v", job.ArtifactSitePath)
	}
	if job.Error == nil || *job.Error != "publish stage publish: exporter exploded" {
		t.Fatalf("unexpected error detail: %#v", job.Error)
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("ListJobAttempts() error = %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(attempts))
	}
	if attempts[0].ArtifactSitePath == nil || !strings.Contains(*attempts[0].ArtifactSitePath, filepath.Join("runs", resp.ID+"--attempt-001.site")) {
		t.Fatalf("expected retained attempt-local partial site path, got %#v", attempts[0].ArtifactSitePath)
	}
}

func TestCreateJobPublishesJobCreatedEvent(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	events, unsubscribe := rt.events.Subscribe()
	defer unsubscribe()

	req := httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/events"}`))
	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("unexpected status: got %d want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp createJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Type != "job.created" || event.JobID != resp.ID {
				continue
			}
			if event.Job.ID != resp.ID {
				t.Fatalf("unexpected job id in event: got %s want %s", event.Job.ID, resp.ID)
			}
			if event.Attempt == nil || event.Attempt.AttemptNumber != 1 {
				t.Fatalf("expected attempt 1 in event, got %#v", event.Attempt)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for job.created event")
		}
	}
}

func TestEventsHandlerStreamsPublishedEvents(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	// Serve the handler over a real HTTP server and read the stream
	// through the connection: polling httptest.ResponseRecorder.Body
	// while the handler goroutine writes to it is a data race.
	srv := httptest.NewServer(http.HandlerFunc(rt.eventsHandler))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	if err != nil {
		t.Fatalf("build /events request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events failed: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("unexpected content type: got %q want %q", got, "text/event-stream")
	}

	var body strings.Builder
	buf := make([]byte, 1024)
	published := false
	for {
		n, readErr := resp.Body.Read(buf)
		body.Write(buf[:n])
		// The ": connected" comment is written after the handler
		// subscribed to the hub, so publishing once it arrives cannot
		// race the subscription and drop the event.
		if !published && strings.Contains(body.String(), ": connected") {
			rt.publishStateChangeEvent(StateChangeEvent{
				Type:  "job.updated",
				JobID: "job-123",
				At:    nowUTCString(),
				Job: Job{
					ID:        "job-123",
					Stage:     "record",
					State:     "running",
					CreatedAt: nowUTCString(),
					UpdatedAt: nowUTCString(),
				},
			})
			published = true
		}
		got := body.String()
		if strings.Contains(got, "event: job.updated") && strings.Contains(got, `"job_id":"job-123"`) {
			break
		}
		if readErr != nil {
			t.Fatalf("event stream ended before job.updated arrived: read error = %v, body = %q", readErr, got)
		}
	}
}

func newCLITestRuntime(t *testing.T) (*Runtime, func(), string, string) {
	return newCLITestRuntimeWithContext(t, context.Background())
}

func newCLITestRuntimeWithContext(t *testing.T, ctx context.Context) (*Runtime, func(), string, string) {
	t.Helper()
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)
	// A test that wants a different capability must set it AFTER this helper —
	// setting it before is silently overwritten here, which made one CPU-path
	// assertion pass on a laptop and fail on a GPU host.
	t.Setenv(envSTTCUDACapable, "1")

	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "cassini.log")
	startedPath := filepath.Join(tmp, "record.started")
	t.Setenv("FAKE_CASSINI_LOG", logPath)
	t.Setenv("FAKE_RECORD_STARTED_FILE", startedPath)
	cassiniBin := filepath.Join(tmp, "fake-cassini.sh")
	if err := os.WriteFile(cassiniBin, []byte(`#!/bin/sh
set -eu
LOG_FILE="${FAKE_CASSINI_LOG:-}"
if [ -n "$LOG_FILE" ]; then
  printf '%s\n' "$*" >> "$LOG_FILE"
fi
cmd="${1:-}"
if [ "$#" -gt 0 ]; then
  shift
fi
write_run() {
  out="$1"
  mkdir -p "$out"
  printf 'recording' > "$out/recording.mkv"
  cat > "$out/cassini.json" <<'EOF'
{"kind":"run","version":"cassini.run.v1","state":"ready","stage":"ready","source_mode":"talk","recording":{"path":"recording.mkv","format":"mkv"}}
EOF
}
finalize_run() {
  out="$1"
  printf '%s\n' "talk recorder stopping: stop requested"
  if [ -n "${FAKE_RECORD_FINALIZE_DELAY:-}" ]; then
    sleep "$FAKE_RECORD_FINALIZE_DELAY"
  fi
  write_run "$out"
  exit 0
}
case "$cmd" in
  doctor)
    if [ "${FAKE_CASSINI_DOCTOR_HANG:-0}" = "1" ]; then
      sleep 60
    fi
    if [ "${FAKE_CASSINI_DOCTOR_FAIL:-0}" = "1" ]; then
      exit 1
    fi
    exit 0
    ;;
  record)
    out=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --out)
          out="$2"
          shift 2
          ;;
        *)
          shift
          ;;
      esac
    done
    [ -n "$out" ]
    printf '%s\n' "talk recorder running: room=fake duration_limit=0s stop_when_room_empty=true room_empty_grace=30s final=$out/recording.mkv segments=$out/segments"
    if [ -n "${FAKE_RECORD_STARTED_FILE:-}" ]; then
      : > "$FAKE_RECORD_STARTED_FILE"
    fi
    if [ "${FAKE_RECORD_IGNORE_TERM:-0}" = "1" ]; then
      trap '' TERM
      while :; do sleep 0.1; done
    fi
    if [ "${FAKE_RECORD_WAIT_FOR_SIGNAL:-0}" = "1" ]; then
      trap 'finalize_run "$out"' TERM
      while :; do sleep 0.1; done
    fi
    write_run "$out"
    exit 0
    ;;
  pack)
    # The seal stage (D-583) packs every built meeting before publish is
    # queued, so a fake cassini without this case stops the whole pipeline
    # there. Deterministic bytes keep the sealed digest reproducible.
    out=""
    prev=""
    for arg in "$@"; do
      if [ "$prev" = "--out" ]; then out="$arg"; fi
      prev="$arg"
    done
    [ -n "$out" ]
    if [ "${FAKE_CASSINI_PACK_FAIL:-0}" = "1" ]; then
      echo "fake pack failure" >&2
      exit 1
    fi
    if [ "${FAKE_CASSINI_PACK_HANG:-0}" = "1" ]; then
      sleep 60
    fi
    mkdir -p "$(dirname "$out")"
    printf 'sealed-opus' > "$out"
    exit 0
    ;;
  *)
    echo "unexpected command: $cmd" >&2
    exit 1
    ;;
esac
`), 0o755); err != nil {
		t.Fatalf("write fake cassini bin: %v", err)
	}
	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	logger := log.New(ioDiscard{}, "", 0)
	rt := NewRuntime(ctx, store, Config{
		RepoRoot:         repoRoot,
		BindAddr:         "127.0.0.1:0",
		DBPath:           filepath.Join(tmp, "jobs.sqlite3"),
		WorkRoot:         filepath.Join(tmp, "jobs"),
		SiteRoot:         filepath.Join(tmp, "site"),
		CassiniBin:       cassiniBin,
		MaxRecordWorkers: 1,
		MaxBuildWorkers:  1,
	}, logger, ioDiscard{}, ioDiscard{})
	rt.buildJobFn = func(ctx context.Context, task buildTask) (string, error) {
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if err := writeReadyMeetingBundleFixture(meetingPath, task.ArtifactRunPath); err != nil {
			return meetingPath, err
		}
		return meetingPath, nil
	}
	rt.publishJobFn = func(ctx context.Context, task publishTask) (string, error) {
		sitePath := attemptSitePath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if err := writeReadySiteBundleFixture(sitePath, currentRoot(rt.cfg.WorkRoot)); err != nil {
			return sitePath, err
		}
		return sitePath, nil
	}
	// Stop the pipeline workers before t.TempDir cleanup removes WorkRoot;
	// a still-running publish or requeue pass writing under it flakes the
	// RemoveAll with "directory not empty" (D-584).
	return rt, func() { cleanupTestRuntime(t, rt, store) }, logPath, startedPath
}

func newTestRuntime(t *testing.T) (*Runtime, func()) {
	t.Helper()
	return newTestRuntimeWithLogger(t, log.New(ioDiscard{}, "", 0))
}

func newTestRuntimeWithLogger(t *testing.T, logger *log.Logger) (*Runtime, func()) {
	t.Helper()
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)
	// A test that wants a different capability must set it AFTER this helper —
	// setting it before is silently overwritten here, which made one CPU-path
	// assertion pass on a laptop and fail on a GPU host.
	t.Setenv(envSTTCUDACapable, "1")

	tmp := t.TempDir()
	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	rt := NewRuntime(context.Background(), store, Config{
		RepoRoot:         repoRoot,
		BindAddr:         "127.0.0.1:0",
		DBPath:           filepath.Join(tmp, "jobs.sqlite3"),
		WorkRoot:         filepath.Join(tmp, "jobs"),
		SiteRoot:         filepath.Join(tmp, "site"),
		CassiniBin:       filepath.Join(repoRoot, "bin", "cassini"),
		MaxRecordWorkers: 1,
		MaxBuildWorkers:  1,
	}, logger, ioDiscard{}, ioDiscard{})
	rt.recordJobFn = func(ctx context.Context, job Job, req TriggerRequest) (recordResult, error) {
		runPath := attemptRunPath(rt.cfg.WorkRoot, job.ID, job.CurrentAttemptNumber)
		bundle, err := PrepareRunBundle(runPath, false)
		if err != nil {
			return recordResult{}, err
		}
		if err := os.WriteFile(bundle.RecordingPath, []byte("fake-mkv"), 0o644); err != nil {
			return recordResult{}, err
		}
		if err := FinalizeRunBundle(bundle, RunManifest{SourceMode: "talk", RecorderName: req.GuestName}); err != nil {
			return recordResult{}, err
		}
		return recordResult{ArtifactRunPath: bundle.RootDir, StopReason: "room_empty", ExitCode: intPtr(0), StopDetail: "room empty for 30s after remote participants left"}, nil
	}
	rt.buildJobFn = func(ctx context.Context, task buildTask) (string, error) {
		meetingPath := attemptMeetingPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if err := writeReadyMeetingBundleFixture(meetingPath, task.ArtifactRunPath); err != nil {
			return meetingPath, err
		}
		return meetingPath, nil
	}
	rt.sealJobFn = writeSealedOpusFixture(rt)
	rt.publishJobFn = func(ctx context.Context, task publishTask) (string, error) {
		sitePath := attemptSitePath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if err := writeReadySiteBundleFixture(sitePath, currentRoot(rt.cfg.WorkRoot)); err != nil {
			return sitePath, err
		}
		return sitePath, nil
	}
	// Stop the pipeline workers before t.TempDir cleanup removes WorkRoot;
	// a still-running publish or requeue pass writing under it flakes the
	// RemoveAll with "directory not empty" (D-584).
	return rt, func() { cleanupTestRuntime(t, rt, store) }
}

func cleanupTestRuntime(t *testing.T, rt *Runtime, store *Store) {
	t.Helper()
	// Shutdown cancels the runtime and drains the registered pipeline workers.
	// Record jobs have their own wait group, so wait for them as well before the
	// TempDir cleanup removes files they may still be creating.
	rt.Shutdown()
	if !rt.WaitForRecordJobs(testWaitTimeout) {
		t.Error("timed out waiting for record jobs during test cleanup")
	}
	if err := store.Close(); err != nil {
		t.Errorf("close test store: %v", err)
	}
}

// writeSealedOpusFixture stands in for `cassini pack`: it writes the attempt's
// sealed `.opus` without an ffmpeg subprocess, the same way buildJobFn and
// publishJobFn stand in for `cassini build` and `cassini publish`. The bytes
// vary per attempt so a test can tell two seals apart.
func writeSealedOpusFixture(rt *Runtime) func(context.Context, sealTask) (string, error) {
	return func(ctx context.Context, task sealTask) (string, error) {
		opusPath := attemptOpusPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber)
		if err := os.MkdirAll(filepath.Dir(opusPath), 0o755); err != nil {
			return "", err
		}
		body := fmt.Sprintf("sealed-opus %s attempt %d", task.JobID, task.AttemptNumber)
		if err := os.WriteFile(opusPath, []byte(body), 0o644); err != nil {
			return "", err
		}
		return opusPath, nil
	}
}

// testWaitTimeout bounds the store-polling helpers below. CI runs the full
// -race suite (150+ tests) under heavy CPU contention, which starves the
// in-process pipeline worker driving these jobs; the original 5s deadline was
// too tight and flaked intermittently under that load (e.g.
// TestTalkDeliveryFailureKeepsPipelineAndRerunRedelivers, and the D-443
// second-stop race). 30s is generous on a loaded shared runner yet still fails
// fast on a genuine hang. The 20ms poll interval is unchanged, so a passing
// test still returns immediately — only the failure ceiling moves.
const testWaitTimeout = 30 * time.Second

func waitForJobState(t *testing.T, store *Store, id, wantState string) Job {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
	for time.Now().Before(deadline) {
		job, err := store.GetJob(context.Background(), id)
		if err == nil && job.State == wantState {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	job, err := store.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJob(%s) error = %v", id, err)
	}
	t.Fatalf("job %s did not reach state %q, last job = %#v", id, wantState, job)
	return Job{}
}

func waitForRecordState(t *testing.T, store *Store, id, wantState string) Job {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
	for time.Now().Before(deadline) {
		job, err := store.GetJob(context.Background(), id)
		if err == nil && job.Stage == "record" && job.State == wantState {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	job, err := store.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJob(%s) error = %v", id, err)
	}
	t.Fatalf("job %s did not reach record/%q, last job = %#v", id, wantState, job)
	return Job{}
}

func waitForJobStageState(t *testing.T, store *Store, id, wantStage string, wantStates ...string) Job {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
	for time.Now().Before(deadline) {
		job, err := store.GetJob(context.Background(), id)
		if err == nil && job.Stage == wantStage {
			if len(wantStates) == 0 {
				return job
			}
			for _, wantState := range wantStates {
				if job.State == wantState {
					return job
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	job, err := store.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJob(%s) error = %v", id, err)
	}
	t.Fatalf("job %s did not reach %s/%v, last job = %#v", id, wantStage, wantStates, job)
	return Job{}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("file %s was not created in time", path)
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func migrationVersions(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.Query(`SELECT version FROM schema_migrations ORDER BY version ASC`)
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	defer rows.Close()

	var versions []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			t.Fatalf("scan schema_migrations: %v", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate schema_migrations: %v", err)
	}
	return versions
}

func sqliteTableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var found string
	err := db.QueryRow(`
SELECT name
FROM sqlite_master
WHERE type = 'table' AND name = ?
LIMIT 1`, name).Scan(&found)
	if err == nil {
		return true
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	t.Fatalf("query sqlite_master for %s: %v", name, err)
	return false
}

func seedLegacyV1Database(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY NOT NULL,
  provider TEXT NOT NULL,
  request_json TEXT NOT NULL,
  stage TEXT NOT NULL,
  state TEXT NOT NULL,
  artifact_run_path TEXT,
  artifact_meeting_path TEXT,
  artifact_site_path TEXT,
  error TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  record_queued_at TEXT,
  record_started_at TEXT,
  record_finished_at TEXT,
  build_queued_at TEXT,
  build_started_at TEXT,
  build_finished_at TEXT,
  publish_queued_at TEXT,
  publish_started_at TEXT,
  publish_finished_at TEXT,
  interrupted_at TEXT,
  completed_at TEXT
);
CREATE INDEX IF NOT EXISTS jobs_created_desc ON jobs(created_at DESC, id DESC);
INSERT INTO jobs (
  id, provider, request_json, stage, state,
  created_at, updated_at, record_queued_at
) VALUES (
  'legacy-job', 'nextcloud-talk', '{"platform":"nextcloud-talk","url":"https://example.test/legacy"}', 'record', 'queued',
  '2026-04-29T10:00:00Z', '2026-04-29T10:00:00Z', '2026-04-29T10:00:00Z'
);
`); err != nil {
		t.Fatalf("seed legacy V1 database: %v", err)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func makeFakeOperatorRepoRoot(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, "cassini-go-recorder"), 0o755); err != nil {
		t.Fatalf("mkdir fake cassini-go-recorder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "cassini-go-recorder", "go.mod"), []byte("module fake\n"), 0o644); err != nil {
		t.Fatalf("write fake go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "bin", "cassini"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake bin/cassini: %v", err)
	}
	return repoRoot
}

func writeReadyMeetingBundleFixture(meetingDir string, sourcePath string) error {
	if err := os.MkdirAll(meetingDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "meeting.webm"), []byte("webm"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "transcript.words.v1.json"), []byte(`{"version":"transcript.words.v1"}`), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "manifest.json"), []byte(`{"version":"cassini.meeting-artifact.v1"}`), 0o644); err != nil {
		return err
	}
	manifest := `{
  "kind": "meeting",
  "version": "cassini.meeting.v1",
  "state": "ready",
  "stage": "ready",
  "source_kind": "run",
  "source_path": "` + sourcePath + `",
  "artifact_manifest": "manifest.json",
  "files": {
    "audio": "meeting.webm",
    "transcript": "transcript.words.v1.json",
    "artifact_manifest": "manifest.json"
  }
}`
	return os.WriteFile(filepath.Join(meetingDir, "cassini.json"), []byte(manifest), 0o644)
}

func writeReadySiteBundleFixture(siteDir string, sourcePath string) error {
	if err := os.MkdirAll(filepath.Join(siteDir, "meetings", "demo"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(siteDir, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(siteDir, "catalog.json"), []byte(`{"meetings":[{"id":"demo"}]}`), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(siteDir, "meetings", "demo", "manifest.json"), []byte(`{}`), 0o644); err != nil {
		return err
	}
	manifest := `{
  "kind": "site",
  "version": "cassini.site.v1",
  "state": "ready",
  "stage": "ready",
  "source_path": "` + sourcePath + `",
  "catalog_path": "catalog.json",
  "meeting_count": 1
}`
	return os.WriteFile(filepath.Join(siteDir, "cassini.json"), []byte(manifest), 0o644)
}

type seededJobRow struct {
	ID          string
	Stage       string
	State       string
	CreatedAt   string
	CompletedAt *string
}

func insertJob(t *testing.T, db *sql.DB, id, createdAt string) {
	t.Helper()
	seedJobRow(t, db, seededJobRow{ID: id, Stage: "record", State: "queued", CreatedAt: createdAt})
}

func seedJobRow(t *testing.T, db *sql.DB, row seededJobRow) {
	t.Helper()
	completedAt := any(nil)
	if row.CompletedAt != nil {
		completedAt = *row.CompletedAt
	}
	if _, err := db.Exec(`
INSERT INTO jobs (
  id, provider, request_json, stage, state,
  created_at, updated_at, completed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID,
		"nextcloud-talk",
		`{"platform":"nextcloud-talk","url":"https://example.test/call"}`,
		row.Stage,
		row.State,
		row.CreatedAt,
		row.CreatedAt,
		completedAt,
	); err != nil {
		t.Fatalf("insert job %s: %v", row.ID, err)
	}
	if _, err := db.Exec(`
INSERT INTO job_attempts (
  job_id, attempt_number, trigger_kind, request_json,
  stage, state,
  created_at, updated_at, completed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID,
		1,
		"initial",
		`{"platform":"nextcloud-talk","url":"https://example.test/call"}`,
		row.Stage,
		row.State,
		row.CreatedAt,
		row.CreatedAt,
		completedAt,
	); err != nil {
		t.Fatalf("insert initial attempt for %s: %v", row.ID, err)
	}
}

func mustGetJob(t *testing.T, store *Store, id string) Job {
	t.Helper()
	job, err := store.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJob(%s) error = %v", id, err)
	}
	return job
}

func strPtr(v string) *string { return &v }

func intPtr(v int) *int { return &v }
