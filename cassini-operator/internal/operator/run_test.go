package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestStartupMarksIncompleteJobsInterruptedAndPreservesStage(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	seedJobRow(t, rt.store.db, seededJobRow{ID: "queued-record", Stage: "record", State: "queued", CreatedAt: "2026-04-29T10:00:00Z"})
	seedJobRow(t, rt.store.db, seededJobRow{ID: "running-build", Stage: "build", State: "running", CreatedAt: "2026-04-29T10:01:00Z"})
	seedJobRow(t, rt.store.db, seededJobRow{ID: "queued-publish", Stage: "publish", State: "queued", CreatedAt: "2026-04-29T10:02:00Z"})
	seedJobRow(t, rt.store.db, seededJobRow{ID: "done-success", Stage: "done", State: "succeeded", CreatedAt: "2026-04-29T10:03:00Z", CompletedAt: strPtr("2026-04-29T10:04:00Z")})
	seedJobRow(t, rt.store.db, seededJobRow{ID: "done-failed", Stage: "done", State: "failed", CreatedAt: "2026-04-29T10:05:00Z", CompletedAt: strPtr("2026-04-29T10:06:00Z")})

	interruptedAt := "2026-04-29T11:00:00Z"
	count, err := rt.store.MarkIncompleteJobsInterrupted(context.Background(), interruptedAt)
	if err != nil {
		t.Fatalf("MarkIncompleteJobsInterrupted() error = %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}

	queuedRecord := mustGetJob(t, rt.store, "queued-record")
	if queuedRecord.Stage != "record" || queuedRecord.State != "interrupted" {
		t.Fatalf("unexpected queued record job = %#v", queuedRecord)
	}
	if queuedRecord.InterruptedAt == nil || *queuedRecord.InterruptedAt != interruptedAt {
		t.Fatalf("unexpected interrupted_at for queued record = %#v", queuedRecord.InterruptedAt)
	}

	runningBuild := mustGetJob(t, rt.store, "running-build")
	if runningBuild.Stage != "build" || runningBuild.State != "interrupted" {
		t.Fatalf("unexpected running build job = %#v", runningBuild)
	}

	queuedPublish := mustGetJob(t, rt.store, "queued-publish")
	if queuedPublish.Stage != "publish" || queuedPublish.State != "interrupted" {
		t.Fatalf("unexpected queued publish job = %#v", queuedPublish)
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

func TestCreateJobReturnsULIDAndCompletesPublishStage(t *testing.T) {
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
	if job.Stage != "done" {
		t.Fatalf("stage = %q, want done", job.Stage)
	}
	if job.ArtifactRunPath == nil {
		t.Fatalf("expected artifact_run_path to be set")
	}
	if job.ArtifactMeetingPath == nil {
		t.Fatalf("expected artifact_meeting_path to be set")
	}
	if job.ArtifactSitePath == nil {
		t.Fatalf("expected artifact_site_path to be set")
	}
	if job.BuildQueuedAt == nil || job.BuildStartedAt == nil || job.BuildFinishedAt == nil {
		t.Fatalf("expected build timestamps to be set, got job=%#v", job)
	}
	if job.PublishQueuedAt == nil || job.PublishStartedAt == nil || job.PublishFinishedAt == nil {
		t.Fatalf("expected publish timestamps to be set, got job=%#v", job)
	}
	if _, err := os.Stat(filepath.Join(*job.ArtifactRunPath, "recording.mkv")); err != nil {
		t.Fatalf("expected recording.mkv in run bundle: %v", err)
	}
	meetingManifestPath := filepath.Join(*job.ArtifactMeetingPath, "cassini.json")
	raw, err := os.ReadFile(meetingManifestPath)
	if err != nil {
		t.Fatalf("read meeting manifest: %v", err)
	}
	if !strings.Contains(string(raw), `"kind": "meeting"`) {
		t.Fatalf("unexpected meeting manifest: %s", string(raw))
	}
	siteManifestPath := filepath.Join(*job.ArtifactSitePath, "cassini.json")
	raw, err = os.ReadFile(siteManifestPath)
	if err != nil {
		t.Fatalf("read site manifest: %v", err)
	}
	if !strings.Contains(string(raw), `"kind": "site"`) {
		t.Fatalf("unexpected site manifest: %s", string(raw))
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
	logText := readFileString(t, logPath)
	if !strings.Contains(logText, "doctor --target record") {
		t.Fatalf("expected doctor invocation, got %s", logText)
	}
	if !strings.Contains(logText, "record --call https://example.test/live") {
		t.Fatalf("expected record invocation, got %s", logText)
	}
	if !strings.Contains(logText, "--name CassiniRecorder") {
		t.Fatalf("expected default guest name flag, got %s", logText)
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
		"--duration 12",
		"--stop-when-room-empty=false",
		"--room-empty-grace 7.5",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected %q in log, got %s", want, logText)
		}
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
	if stopRec2.Code != http.StatusAccepted {
		t.Fatalf("second stop status = %d, want %d body=%s", stopRec2.Code, http.StatusAccepted, stopRec2.Body.String())
	}

	job := waitForJobState(t, rt.store, resp.ID, "succeeded")
	if job.Stage != "done" {
		t.Fatalf("stage = %q, want done", job.Stage)
	}
	logText := readFileString(t, logPath)
	if !strings.Contains(logText, "record --call https://example.test/stop-me") {
		t.Fatalf("expected record invocation, got %s", logText)
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
	rt.recordJobFn = func(ctx context.Context, job Job, req TriggerRequest) (string, error) {
		defer close(done)
		<-block
		return filepath.Join(rt.cfg.WorkRoot, job.ID+".run"), nil
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
}

func TestBuildFailurePersistsLightweightErrorDetail(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rt.buildJobFn = func(ctx context.Context, task buildTask) (string, error) {
		meetingPath := filepath.Join(rt.cfg.WorkRoot, task.JobID+".meeting")
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
	if job.ArtifactMeetingPath == nil {
		t.Fatalf("expected artifact_meeting_path for partial bundle")
	}
	if job.Error == nil || *job.Error != "build stage build: transcriber exploded" {
		t.Fatalf("unexpected error detail: %#v", job.Error)
	}
}

func TestPublishFailurePersistsLightweightErrorDetail(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rt.publishJobFn = func(ctx context.Context, task publishTask) (string, error) {
		sitePath := rt.cfg.SiteRoot
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
	if job.ArtifactSitePath == nil {
		t.Fatalf("expected artifact_site_path for partial bundle")
	}
	if job.Error == nil || *job.Error != "publish stage publish: exporter exploded" {
		t.Fatalf("unexpected error detail: %#v", job.Error)
	}
}

func newCLITestRuntime(t *testing.T) (*Runtime, func(), string, string) {
	t.Helper()
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)

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
case "$cmd" in
  doctor)
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
    if [ -n "${FAKE_RECORD_STARTED_FILE:-}" ]; then
      : > "$FAKE_RECORD_STARTED_FILE"
    fi
    if [ "${FAKE_RECORD_WAIT_FOR_SIGNAL:-0}" = "1" ]; then
      trap 'write_run "$out"; exit 0' TERM
      while :; do sleep 0.1; done
    fi
    write_run "$out"
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
	rt := NewRuntime(context.Background(), store, Config{
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
		meetingPath := filepath.Join(rt.cfg.WorkRoot, task.JobID+".meeting")
		if err := writeReadyMeetingBundleFixture(meetingPath, task.ArtifactRunPath); err != nil {
			return meetingPath, err
		}
		return meetingPath, nil
	}
	rt.publishJobFn = func(ctx context.Context, task publishTask) (string, error) {
		if err := writeReadySiteBundleFixture(rt.cfg.SiteRoot, rt.cfg.WorkRoot); err != nil {
			return rt.cfg.SiteRoot, err
		}
		return rt.cfg.SiteRoot, nil
	}
	return rt, func() { _ = store.Close() }, logPath, startedPath
}

func newTestRuntime(t *testing.T) (*Runtime, func()) {
	t.Helper()
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	t.Setenv("CASSINI_REPO_ROOT", repoRoot)

	tmp := t.TempDir()
	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	logger := log.New(ioDiscard{}, "", 0)
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
	rt.recordJobFn = func(ctx context.Context, job Job, req TriggerRequest) (string, error) {
		runPath := filepath.Join(rt.cfg.WorkRoot, job.ID+".run")
		bundle, err := PrepareRunBundle(runPath, false)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(bundle.RecordingPath, []byte("fake-mkv"), 0o644); err != nil {
			return "", err
		}
		if err := FinalizeRunBundle(bundle, RunManifest{SourceMode: "talk", RecorderName: req.GuestName}); err != nil {
			return "", err
		}
		return bundle.RootDir, nil
	}
	rt.buildJobFn = func(ctx context.Context, task buildTask) (string, error) {
		meetingPath := filepath.Join(rt.cfg.WorkRoot, task.JobID+".meeting")
		if err := writeReadyMeetingBundleFixture(meetingPath, task.ArtifactRunPath); err != nil {
			return meetingPath, err
		}
		return meetingPath, nil
	}
	rt.publishJobFn = func(ctx context.Context, task publishTask) (string, error) {
		if err := writeReadySiteBundleFixture(rt.cfg.SiteRoot, rt.cfg.WorkRoot); err != nil {
			return rt.cfg.SiteRoot, err
		}
		return rt.cfg.SiteRoot, nil
	}
	return rt, func() { _ = store.Close() }
}

func waitForJobState(t *testing.T, store *Store, id, wantState string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
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
	deadline := time.Now().Add(5 * time.Second)
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

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
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
