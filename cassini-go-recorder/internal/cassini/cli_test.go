package cassini

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/config"
)

func TestRootHelpExitsZero(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Cassini is the product-oriented CLI") {
		t.Fatalf("expected root usage in stdout, got %q", stdout.String())
	}
}

func TestRecordHelpExitsZero(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"record", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cassini record --call") {
		t.Fatalf("expected record usage in stderr, got %q", stderr.String())
	}
}

func TestRecordRejectsSimulatePortableOutput(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"record", "--simulate", "--out", "demo.opus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--simulate currently only supports .run output") {
		t.Fatalf("expected simulate portable validation error, got %q", stderr.String())
	}
}

func TestDoctorHelpExitsZero(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"doctor", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cassini doctor") {
		t.Fatalf("expected doctor usage in stderr, got %q", stderr.String())
	}
}

func TestDevHelpExitsZero(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"dev", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Cassini dev commands") {
		t.Fatalf("expected dev usage in stdout, got %q", stdout.String())
	}
}

func TestInspectRunBundleUsesBundleManifest(t *testing.T) {
	tmp := t.TempDir()
	runDir := filepath.Join(tmp, "demo.run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "recording.csr"), []byte("not-a-real-archive"), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "cassini.json"), []byte(`{
  "kind": "run",
  "version": "cassini.run.v1",
  "source_mode": "simulate",
  "recording": {
    "path": "recording.csr",
    "format": "csr"
  }
}
`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"inspect", runDir}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected inspect to fail on invalid archive data")
	}
	if !strings.Contains(stdout.String(), "run="+runDir) {
		t.Fatalf("expected run bundle summary, got stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "inspect failed: read archive:") {
		t.Fatalf("expected archive read failure, got stderr=%q", stderr.String())
	}
}

func TestInspectHelpIncludesMeetingAndSite(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"inspect", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cassini inspect ./meetings/meeting.meeting") {
		t.Fatalf("expected meeting usage in help, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "cassini inspect ./site") {
		t.Fatalf("expected site usage in help, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "cassini inspect ./archive/meeting.opus") {
		t.Fatalf("expected opus usage in help, got %q", stderr.String())
	}
}

func TestBuildHelpExitsZero(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"build", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cassini build") {
		t.Fatalf("expected build usage in stderr, got %q", stderr.String())
	}
}

func TestBuildUsesRunnerOverrideAndWritesMeetingManifest(t *testing.T) {
	tmp := t.TempDir()
	runner := filepath.Join(tmp, "fake-runner.sh")
	if err := os.WriteFile(runner, []byte(`#!/usr/bin/env bash
set -euo pipefail
OUTPUT_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
mkdir -p "$OUTPUT_DIR"
printf 'fake' >"$OUTPUT_DIR/meeting.webm"
printf '{}' >"$OUTPUT_DIR/transcript.words.v1.json"
printf '{}' >"$OUTPUT_DIR/manifest.json"
`), 0o755); err != nil {
		t.Fatalf("write fake runner: %v", err)
	}

	input := filepath.Join(tmp, "source.mkv")
	if err := os.WriteFile(input, []byte("fake-mkv"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	cacheRoot := filepath.Join(tmp, "cache")
	t.Setenv("CASSINI_TRANSCRIBER_RUNNER", runner)
	t.Setenv("CASSINI_CACHE_ROOT", cacheRoot)

	outDir := filepath.Join(tmp, "meeting.meeting")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"build", input, "--out", outDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected build success, got code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}

	raw, err := os.ReadFile(filepath.Join(outDir, "cassini.json"))
	if err != nil {
		t.Fatalf("read meeting manifest: %v", err)
	}
	if !strings.Contains(string(raw), `"kind": "meeting"`) {
		t.Fatalf("expected meeting manifest, got %q", string(raw))
	}
	if !strings.Contains(stdout.String(), "meeting -> "+outDir) {
		t.Fatalf("expected meeting output path, got stdout=%q", stdout.String())
	}
}

func TestBuildPortableUsesRunnerOverrideAndWritesPortableMeeting(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	runner := writePortableMeetingRunner(t, tmp)

	input := filepath.Join(tmp, "source.mkv")
	if err := os.WriteFile(input, []byte("fake-mkv"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	cacheRoot := filepath.Join(tmp, "cache")
	t.Setenv("CASSINI_TRANSCRIBER_RUNNER", runner)
	t.Setenv("CASSINI_CACHE_ROOT", cacheRoot)

	outFile := filepath.Join(tmp, "Weekly Sync.opus")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"build", input, "--out", outFile}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected build success, got code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "portable_meeting -> "+outFile) {
		t.Fatalf("expected portable meeting output path, got stdout=%q", stdout.String())
	}

	var inspectOut bytes.Buffer
	var inspectErr bytes.Buffer
	code = Run(context.Background(), []string{"inspect", outFile}, &inspectOut, &inspectErr)
	if code != 0 {
		t.Fatalf("inspect portable meeting failed: code=%d stderr=%q stdout=%q", code, inspectErr.String(), inspectOut.String())
	}
	if !strings.Contains(inspectOut.String(), "cassini=ok") {
		t.Fatalf("expected portable meeting integrity ok, got %q", inspectOut.String())
	}
}

func TestBuildPortableNoopsWhenOutputAlreadyReady(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	successRunner := writePortableMeetingRunner(t, tmp)
	failingRunner := filepath.Join(tmp, "fail-runner.sh")
	if err := os.WriteFile(failingRunner, []byte("#!/usr/bin/env bash\nexit 9\n"), 0o755); err != nil {
		t.Fatalf("write failing runner: %v", err)
	}

	input := filepath.Join(tmp, "source.mkv")
	if err := os.WriteFile(input, []byte("fake-mkv"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	cacheRoot := filepath.Join(tmp, "cache")
	t.Setenv("CASSINI_CACHE_ROOT", cacheRoot)
	t.Setenv("CASSINI_TRANSCRIBER_RUNNER", successRunner)

	outFile := filepath.Join(tmp, "Weekly Sync.opus")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"build", input, "--out", outFile}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected first build success, got code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}

	t.Setenv("CASSINI_TRANSCRIBER_RUNNER", failingRunner)
	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"build", input, "--out", outFile}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected no-op build success, got code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "portable_meeting -> "+outFile+" (already ready)") {
		t.Fatalf("expected already-ready message, got stdout=%q", stdout.String())
	}
}

func TestRecordPortableWritesPortableMeetingArtifact(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	runner := writePortableMeetingRunner(t, tmp)
	cacheRoot := filepath.Join(tmp, "cache")
	t.Setenv("CASSINI_TRANSCRIBER_RUNNER", runner)
	t.Setenv("CASSINI_CACHE_ROOT", cacheRoot)

	prevRecorder := runRecorderApp
	runRecorderApp = func(_ context.Context, cfg config.Config) error {
		return os.WriteFile(cfg.OutputPath, []byte("fake-mkv"), 0o644)
	}
	defer func() {
		runRecorderApp = prevRecorder
	}()

	outFile := filepath.Join(tmp, "Recorded Meeting.opus")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"record", "--call", "https://example.test/room/demo", "--out", outFile}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected record success, got code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "[4/5] Transcribing and preparing meeting artifact") {
		t.Fatalf("expected processing progress, got stdout=%q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "portable_meeting -> "+outFile) {
		t.Fatalf("expected portable meeting output path, got stdout=%q", stdout.String())
	}

	var inspectOut bytes.Buffer
	var inspectErr bytes.Buffer
	code = Run(context.Background(), []string{"inspect", outFile}, &inspectOut, &inspectErr)
	if code != 0 {
		t.Fatalf("inspect recorded portable meeting failed: code=%d stderr=%q stdout=%q", code, inspectErr.String(), inspectOut.String())
	}
	if !strings.Contains(inspectOut.String(), "cassini=ok") {
		t.Fatalf("expected portable meeting integrity ok, got %q", inspectOut.String())
	}
}

func TestRecordPortableResumesFromReadyRunAfterBuildFailure(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	failingRunner := filepath.Join(tmp, "fail-runner.sh")
	if err := os.WriteFile(failingRunner, []byte(`#!/usr/bin/env bash
set -euo pipefail
echo "boom" >&2
exit 7
`), 0o755); err != nil {
		t.Fatalf("write failing runner: %v", err)
	}
	successRunner := writePortableMeetingRunner(t, tmp)

	cacheRoot := filepath.Join(tmp, "cache")
	t.Setenv("CASSINI_CACHE_ROOT", cacheRoot)
	t.Setenv("CASSINI_TRANSCRIBER_RUNNER", failingRunner)

	prevRecorder := runRecorderApp
	recordCalls := 0
	runRecorderApp = func(_ context.Context, cfg config.Config) error {
		recordCalls++
		return os.WriteFile(cfg.OutputPath, []byte("fake-mkv"), 0o644)
	}
	defer func() {
		runRecorderApp = prevRecorder
	}()

	outFile := filepath.Join(tmp, "Recorded Meeting.opus")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"record", "--call", "https://example.test/room/demo", "--out", outFile}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected first record to fail, stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if recordCalls != 1 {
		t.Fatalf("expected one recording attempt, got %d", recordCalls)
	}
	if !strings.Contains(stderr.String(), "work_dir -> ") {
		t.Fatalf("expected work_dir path on failure, got stderr=%q", stderr.String())
	}

	t.Setenv("CASSINI_TRANSCRIBER_RUNNER", successRunner)
	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"record", "--call", "https://example.test/room/demo", "--out", outFile}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected resumed record success, got code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if recordCalls != 1 {
		t.Fatalf("expected recorder not to rerun, got %d calls", recordCalls)
	}
	if !strings.Contains(stdout.String(), "resume reusing recorded meeting") {
		t.Fatalf("expected run reuse message, got stdout=%q", stdout.String())
	}

	var inspectOut bytes.Buffer
	var inspectErr bytes.Buffer
	code = Run(context.Background(), []string{"inspect", outFile}, &inspectOut, &inspectErr)
	if code != 0 {
		t.Fatalf("inspect resumed portable meeting failed: code=%d stderr=%q stdout=%q", code, inspectErr.String(), inspectOut.String())
	}
	if !strings.Contains(inspectOut.String(), "cassini=ok") {
		t.Fatalf("expected portable meeting integrity ok, got %q", inspectOut.String())
	}
}

func TestBuildPortableResumesFromReadyMeetingWorkspace(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	input := filepath.Join(tmp, "source.mkv")
	if err := os.WriteFile(input, []byte("fake-mkv"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	outFile := filepath.Join(tmp, "Recovered Meeting.opus")
	workspace, err := ensurePortableWorkspace(outFile)
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	sourcePath, err := filepath.Abs(input)
	if err != nil {
		t.Fatalf("abs input: %v", err)
	}
	if err := writeReadyMeetingBundleFixture(workspace.MeetingDir, sourcePath); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}

	failingRunner := filepath.Join(tmp, "fail-runner.sh")
	if err := os.WriteFile(failingRunner, []byte("#!/usr/bin/env bash\nexit 9\n"), 0o755); err != nil {
		t.Fatalf("write failing runner: %v", err)
	}
	cacheRoot := filepath.Join(tmp, "cache")
	t.Setenv("CASSINI_CACHE_ROOT", cacheRoot)
	t.Setenv("CASSINI_TRANSCRIBER_RUNNER", failingRunner)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"build", input, "--out", outFile}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected resumed build success, got code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "resume reusing built meeting artifact") {
		t.Fatalf("expected meeting reuse message, got stdout=%q", stdout.String())
	}

	var inspectOut bytes.Buffer
	var inspectErr bytes.Buffer
	code = Run(context.Background(), []string{"inspect", outFile}, &inspectOut, &inspectErr)
	if code != 0 {
		t.Fatalf("inspect rebuilt portable meeting failed: code=%d stderr=%q stdout=%q", code, inspectErr.String(), inspectOut.String())
	}
	if !strings.Contains(inspectOut.String(), "cassini=ok") {
		t.Fatalf("expected portable meeting integrity ok, got %q", inspectOut.String())
	}
}

func TestInspectMeetingBundlePrintsBundleSummary(t *testing.T) {
	tmp := t.TempDir()
	meetingDir := filepath.Join(tmp, "demo.meeting")
	if err := os.MkdirAll(meetingDir, 0o755); err != nil {
		t.Fatalf("mkdir meeting dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "cassini.json"), []byte(`{
  "kind": "meeting",
  "version": "cassini.meeting.v1",
  "source_kind": "mkv",
  "source_path": "/tmp/source.mkv",
  "files": {
    "audio": "meeting.webm",
    "transcript": "transcript.words.v1.json"
  }
}
`), 0o644); err != nil {
		t.Fatalf("write meeting manifest: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"inspect", meetingDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected inspect success, got code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "meeting="+meetingDir) {
		t.Fatalf("expected meeting summary, got stdout=%q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "file=audio path=meeting.webm") {
		t.Fatalf("expected meeting file listing, got stdout=%q", stdout.String())
	}
}

func TestInspectPartialMeetingDirWithoutManifest(t *testing.T) {
	tmp := t.TempDir()
	meetingDir := filepath.Join(tmp, "partial.meeting")
	if err := os.MkdirAll(meetingDir, 0o755); err != nil {
		t.Fatalf("mkdir meeting dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "meeting.webm"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"inspect", meetingDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected inspect success, got code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "status=partial:manifest-missing") {
		t.Fatalf("expected partial status, got stdout=%q", stdout.String())
	}
}

func TestPublishHelpExitsZero(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"publish", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cassini publish") {
		t.Fatalf("expected publish usage in stderr, got %q", stderr.String())
	}
}

func TestPublishUsesExporterOverrideAndWritesSiteManifest(t *testing.T) {
	tmp := t.TempDir()
	exporter := filepath.Join(tmp, "fake-exporter.sh")
	if err := os.WriteFile(exporter, []byte(`#!/usr/bin/env bash
set -euo pipefail
OUTPUT_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
mkdir -p "$OUTPUT_DIR/assets" "$OUTPUT_DIR/meetings/demo"
printf '<html></html>' >"$OUTPUT_DIR/index.html"
printf '{"meetings":[{"id":"demo"}]}' >"$OUTPUT_DIR/catalog.json"
printf '{}' >"$OUTPUT_DIR/meetings/demo/manifest.json"
`), 0o755); err != nil {
		t.Fatalf("write fake exporter: %v", err)
	}

	meetingDir := filepath.Join(tmp, "demo.meeting")
	if err := os.MkdirAll(meetingDir, 0o755); err != nil {
		t.Fatalf("mkdir meeting dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "cassini.json"), []byte(`{
  "kind": "meeting",
  "version": "cassini.meeting.v1",
  "source_kind": "mkv",
  "source_path": "/tmp/source.mkv",
  "files": {
    "audio": "meeting.webm",
    "transcript": "transcript.words.v1.json"
  }
}
`), 0o644); err != nil {
		t.Fatalf("write meeting manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "meeting.webm"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("write meeting webm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "transcript.words.v1.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write artifact manifest: %v", err)
	}

	t.Setenv("CASSINI_EXPORTER_RUNNER", exporter)
	outDir := filepath.Join(tmp, "site")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"publish", meetingDir, "--out", outDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected publish success, got code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}

	raw, err := os.ReadFile(filepath.Join(outDir, "cassini.json"))
	if err != nil {
		t.Fatalf("read site manifest: %v", err)
	}
	if !strings.Contains(string(raw), `"kind": "site"`) {
		t.Fatalf("expected site manifest, got %q", string(raw))
	}
	if !strings.Contains(stdout.String(), "site -> "+outDir) {
		t.Fatalf("expected site output path, got stdout=%q", stdout.String())
	}
}

func TestPublishRejectsPartialMeetingBundle(t *testing.T) {
	tmp := t.TempDir()
	meetingsDir := filepath.Join(tmp, "meetings")
	if err := os.MkdirAll(meetingsDir, 0o755); err != nil {
		t.Fatalf("mkdir meetings dir: %v", err)
	}
	meetingDir := filepath.Join(meetingsDir, "broken.meeting")
	if err := os.MkdirAll(meetingDir, 0o755); err != nil {
		t.Fatalf("mkdir meeting dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "cassini.json"), []byte(`{
  "kind": "meeting",
  "version": "cassini.meeting.v1",
  "state": "failed",
  "stage": "doctor",
  "error": "build blocked by doctor failures"
}
`), 0o644); err != nil {
		t.Fatalf("write meeting manifest: %v", err)
	}

	outDir := filepath.Join(tmp, "site")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"publish", meetingsDir, "--out", outDir}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected publish failure, got stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no ready meeting bundles found") {
		t.Fatalf("expected no-ready-bundles error, got stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "broken.meeting (failed:doctor: build blocked by doctor failures)") {
		t.Fatalf("expected skipped partial bundle details, got stderr=%q", stderr.String())
	}
}

func TestServeHelpExitsZero(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"serve", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cassini serve") {
		t.Fatalf("expected serve usage in stderr, got %q", stderr.String())
	}
}

func TestInspectSiteBundlePrintsBundleSummary(t *testing.T) {
	tmp := t.TempDir()
	siteDir := filepath.Join(tmp, "site")
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatalf("mkdir site dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(siteDir, "cassini.json"), []byte(`{
  "kind": "site",
  "version": "cassini.site.v1",
  "source_path": "/tmp/meetings",
  "catalog_path": "catalog.json",
  "meeting_count": 2
}
`), 0o644); err != nil {
		t.Fatalf("write site manifest: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"inspect", siteDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected inspect success, got code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "site="+siteDir) {
		t.Fatalf("expected site summary, got stdout=%q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "meetings=2") {
		t.Fatalf("expected meeting count, got stdout=%q", stdout.String())
	}
}

func requireFFMediaTools(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}
}

func writePortableMeetingRunner(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "portable-runner.sh")
	if err := os.WriteFile(path, []byte(`#!/usr/bin/env bash
set -euo pipefail
OUTPUT_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
mkdir -p "$OUTPUT_DIR"
ffmpeg -y -v error -f lavfi -i "sine=frequency=660:sample_rate=48000:duration=0.25" -c:a libopus -application voip "$OUTPUT_DIR/meeting.webm"
cat >"$OUTPUT_DIR/transcript.words.v1.json" <<'JSON'
{
  "version": "transcript.words.v1",
  "media": {
    "src": "meeting.webm",
    "durationMs": 250
  },
  "speakers": [
    {
      "id": "spk_host",
      "label": "Host"
    }
  ],
  "segments": [
    {
      "speaker": "spk_host",
      "startMs": 0,
      "endMs": 200,
      "text": "hello team",
      "words": [
        {
          "text": "hello",
          "startMs": 0,
          "endMs": 80
        },
        {
          "text": "team",
          "startMs": 100,
          "endMs": 200
        }
      ]
    }
  ]
}
JSON
cat >"$OUTPUT_DIR/manifest.json" <<'JSON'
{
  "version": "cassini.meeting-artifact.v1",
  "generatedAt": "2026-03-11T10:00:00Z",
  "source": {
    "basename": "source.mkv",
    "durationMs": 250
  },
  "files": {
    "audio": "meeting.webm",
    "transcript": "transcript.words.v1.json"
  },
  "speakerCount": 1,
  "wordCount": 2
}
JSON
`), 0o755); err != nil {
		t.Fatalf("write portable runner: %v", err)
	}
	return path
}

func writeReadyMeetingBundleFixture(meetingDir string, sourcePath string) error {
	if err := os.MkdirAll(meetingDir, 0o755); err != nil {
		return err
	}
	if output, err := exec.Command(
		"ffmpeg",
		"-y",
		"-v", "error",
		"-f", "lavfi",
		"-i", "sine=frequency=660:sample_rate=48000:duration=0.25",
		"-c:a", "libopus",
		"-application", "voip",
		filepath.Join(meetingDir, "meeting.webm"),
	).CombinedOutput(); err != nil {
		return fmt.Errorf("write meeting audio: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "transcript.words.v1.json"), []byte(`{
  "version": "transcript.words.v1",
  "media": {
    "src": "meeting.webm",
    "durationMs": 250
  },
  "speakers": [
    {
      "id": "spk_host",
      "label": "Host"
    }
  ],
  "segments": [
    {
      "speaker": "spk_host",
      "startMs": 0,
      "endMs": 200,
      "text": "hello team",
      "words": [
        {
          "text": "hello",
          "startMs": 0,
          "endMs": 80
        },
        {
          "text": "team",
          "startMs": 100,
          "endMs": 200
        }
      ]
    }
  ]
}
`), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(meetingDir, "manifest.json"), []byte(`{
  "version": "cassini.meeting-artifact.v1",
  "generatedAt": "2026-03-11T10:00:00Z",
  "source": {
    "basename": "source.mkv",
    "durationMs": 250
  },
  "files": {
    "audio": "meeting.webm",
    "transcript": "transcript.words.v1.json"
  },
  "speakerCount": 1,
  "wordCount": 2
}
`), 0o644); err != nil {
		return err
	}
	bundle := MeetingBundle{
		RootDir:      meetingDir,
		ManifestPath: filepath.Join(meetingDir, "cassini.json"),
	}
	return FinalizeMeetingBundle(bundle, MeetingBundleManifest{
		SourceKind: "mkv",
		SourcePath: sourcePath,
	})
}

func TestInspectLooseStaticSiteDir(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "catalog.json"), []byte(`{"meetings":[{"id":"a"},{"id":"b"}]}`), 0o644); err != nil {
		t.Fatalf("write catalog: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"inspect", tmp}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected inspect success, got code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "status=legacy") {
		t.Fatalf("expected legacy site status, got stdout=%q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "meetings=2") {
		t.Fatalf("expected meeting count, got stdout=%q", stdout.String())
	}
}
