package operator

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestBackfillEndToEndExercisesBuildSubprocessEnv exercises the full backfill
// path through the *real* executeBuildCLI subprocess: a seeded completed job
// with a v1-style single-transcript bundle is POSTed to the backfill
// endpoint, the operator queues a build, and a fake bin/cassini script (a)
// records the env it was invoked with so we can assert CASSINI_STT_ADDITIONAL_MODELS
// flowed through, and (b) writes a v2-style two-transcript bundle so that the
// eligibility endpoint no longer lists the job afterwards.
//
// Skipped on non-unix because the fake binary is a /bin/sh script.
func TestBackfillEndToEndExercisesBuildSubprocessEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bin/cassini script requires a unix shell")
	}

	tmp := t.TempDir()
	t.Setenv("CASSINI_REPO_ROOT", tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "cassini-go-recorder"), 0o755); err != nil {
		t.Fatalf("mkdir recorder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "cassini-go-recorder", "go.mod"), []byte("module fake\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	envMarker := filepath.Join(tmp, "build-env.txt")
	// Fake build script: usage `cassini build <run-path> --out <meeting-path>`.
	// Writes a v2-shape manifest.json into the meeting dir and dumps its env
	// (the bit we care about: CASSINI_STT_ADDITIONAL_MODELS) into a marker
	// file the test can read back. Also lays down a cassini.json that the
	// promote step will accept.
	cassiniScript := `#!/bin/sh
set -eu
cmd="$1"
shift
case "$cmd" in
  build)
    run="$1"; shift
    out=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --out) out="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    mkdir -p "$out"
    printf 'CASSINI_STT_ADDITIONAL_MODELS=%s\n' "${CASSINI_STT_ADDITIONAL_MODELS:-}" > ` + envMarker + `
    cat > "$out/manifest.json" <<'EOF'
{
  "files": {
    "audio": "meeting.opus",
    "transcripts": [
      {"id": "parakeet-tdt-0-6b-v3", "role": "raw-asr", "default": true},
      {"id": "parakeet-tdt-0-6b",    "role": "raw-asr"}
    ]
  }
}
EOF
    cat > "$out/cassini.json" <<EOF
{
  "kind": "meeting",
  "version": "cassini.meeting.v1",
  "state": "ready",
  "stage": "ready",
  "source_kind": "run",
  "source_path": "$run"
}
EOF
    : > "$out/meeting.opus"
    ;;
  publish)
    out=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --out) out="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    mkdir -p "$out/meetings/demo"
    : > "$out/index.html"
    printf '{"meetings":[{"id":"demo"}]}\n' > "$out/catalog.json"
    printf '{}\n' > "$out/meetings/demo/manifest.json"
    cat > "$out/cassini.json" <<EOF
{
  "kind": "site",
  "version": "cassini.site.v1",
  "state": "ready",
  "stage": "ready"
}
EOF
    ;;
  *) exit 0 ;;
esac
`
	binPath := filepath.Join(binDir, "cassini")
	if err := os.WriteFile(binPath, []byte(cassiniScript), 0o755); err != nil {
		t.Fatalf("write fake cassini: %v", err)
	}

	store, err := OpenStore(filepath.Join(tmp, "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	logger := log.New(ioDiscard{}, "", 0)
	rt := NewRuntime(context.Background(), store, Config{
		RepoRoot:         tmp,
		BindAddr:         "127.0.0.1:0",
		DBPath:           filepath.Join(tmp, "jobs.sqlite3"),
		WorkRoot:         filepath.Join(tmp, "jobs"),
		SiteRoot:         filepath.Join(tmp, "site"),
		CassiniBin:       binPath,
		MaxRecordWorkers: 1,
		MaxBuildWorkers:  1,
	}, logger, ioDiscard{}, ioDiscard{})

	// Seed a completed job whose canonical meeting bundle is single-transcript
	// (the legacy v1-style case the user wants to backfill).
	jobID := "backfill-e2e"
	runPath := filepath.Join(rt.cfg.WorkRoot, "runs", jobID, "attempt-1")
	if err := os.MkdirAll(runPath, 0o755); err != nil {
		t.Fatalf("mkdir runPath: %v", err)
	}
	bundle, err := PrepareRunBundle(runPath, false)
	if err != nil {
		t.Fatalf("PrepareRunBundle: %v", err)
	}
	if err := os.WriteFile(bundle.RecordingPath, []byte("fake-mkv"), 0o644); err != nil {
		t.Fatalf("write fake recording: %v", err)
	}
	if err := FinalizeRunBundle(bundle, RunManifest{SourceMode: "talk", RecorderName: "tester"}); err != nil {
		t.Fatalf("FinalizeRunBundle: %v", err)
	}
	canonicalRunPath, err := promoteRunBundle(rt.cfg.WorkRoot, bundle.RootDir, jobID)
	if err != nil {
		t.Fatalf("promoteRunBundle: %v", err)
	}

	meetingPath := filepath.Join(rt.cfg.WorkRoot, "meetings", jobID)
	if err := os.MkdirAll(meetingPath, 0o755); err != nil {
		t.Fatalf("mkdir meeting: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingPath, "manifest.json"), []byte(`{"files":{"transcript":"transcript.words.v1.json"}}`), 0o644); err != nil {
		t.Fatalf("write legacy manifest: %v", err)
	}
	if err := insertCompletedJobRow(rt, jobID, canonicalRunPath, meetingPath); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	srv := httptest.NewServer(newHTTPHandler(rt.logger, rt))
	defer srv.Close()

	// Sanity: eligibility lists this job before the backfill runs.
	pre := mustFetchEligible(t, srv.URL)
	if !containsJob(pre, jobID) {
		t.Fatalf("expected %s in eligible list before backfill, got %+v", jobID, pre)
	}

	resp, err := http.Post(srv.URL+"/jobs/"+jobID+"/backfill-transcripts", "application/json", nil)
	if err != nil {
		t.Fatalf("POST backfill: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	// Wait for the rerun's build+publish chain to reach done/succeeded.
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, err := store.GetJob(context.Background(), jobID)
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		if job.Stage == "done" && job.State == "succeeded" {
			break
		}
		if job.Stage == "done" && job.State == "failed" {
			detail := ""
			if job.Error != nil {
				detail = *job.Error
			}
			t.Fatalf("backfill build failed: %s", detail)
		}
		if time.Now().After(deadline) {
			t.Fatalf("backfill did not complete in 10s; last stage/state=%s/%s", job.Stage, job.State)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The fake binary wrote its env into a marker file — assert the backfill
	// trigger kind translated into CASSINI_STT_ADDITIONAL_MODELS.
	envBytes, err := os.ReadFile(envMarker)
	if err != nil {
		t.Fatalf("read env marker: %v", err)
	}
	got := string(envBytes)
	want := "CASSINI_STT_ADDITIONAL_MODELS=" + legacyBackfillAdditionalModel + "\n"
	if got != want {
		t.Fatalf("env marker = %q, want %q", got, want)
	}

	// Eligibility endpoint should no longer list the job now that the
	// canonical meeting bundle was overwritten with a 2-transcript manifest.
	post := mustFetchEligible(t, srv.URL)
	if containsJob(post, jobID) {
		t.Fatalf("expected %s to NOT be eligible after backfill, got %+v", jobID, post)
	}
}

func mustFetchEligible(t *testing.T, baseURL string) []backfillEligibleJob {
	t.Helper()
	resp, err := http.Get(baseURL + "/backfill-transcripts/eligible")
	if err != nil {
		t.Fatalf("GET eligible: %v", err)
	}
	defer resp.Body.Close()
	var body backfillEligibleResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode eligible: %v", err)
	}
	return body.Jobs
}

func containsJob(list []backfillEligibleJob, id string) bool {
	for _, j := range list {
		if j.ID == id {
			return true
		}
	}
	return false
}
