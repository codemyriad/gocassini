package operator

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildEnvForAttemptOverridesAdditionalModels(t *testing.T) {
	rt := &Runtime{}
	t.Setenv("CASSINI_STT_ADDITIONAL_MODELS", "canary-1b") // a different value on the parent
	env := rt.buildEnvForAttempt(buildTask{TriggerKind: TriggerKindBackfillGPU})

	occurrences := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "CASSINI_STT_ADDITIONAL_MODELS=") {
			occurrences++
			want := "CASSINI_STT_ADDITIONAL_MODELS=" + legacyBackfillAdditionalModel
			if kv != want {
				t.Fatalf("env entry = %q, want %q", kv, want)
			}
		}
	}
	if occurrences != 1 {
		t.Fatalf("expected exactly one CASSINI_STT_ADDITIONAL_MODELS entry, got %d", occurrences)
	}
}

func TestBuildEnvForAttemptPassesThroughParentForNonOverrideKinds(t *testing.T) {
	rt := &Runtime{}
	t.Setenv("CASSINI_STT_ADDITIONAL_MODELS", "canary-1b")
	env := rt.buildEnvForAttempt(buildTask{TriggerKind: TriggerKindRerun})
	found := false
	for _, kv := range env {
		if kv == "CASSINI_STT_ADDITIONAL_MODELS=canary-1b" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected parent CASSINI_STT_ADDITIONAL_MODELS to pass through for rerun kind")
	}
}

func TestEnvAdditionsForTriggerKind(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		wantKey string
		wantVal string
	}{
		{name: "backfill-gpu enables legacy CPU model", kind: TriggerKindBackfillGPU, wantKey: "CASSINI_STT_ADDITIONAL_MODELS", wantVal: legacyBackfillAdditionalModel},
		{name: "initial has no additions", kind: TriggerKindInitial},
		{name: "rerun has no additions", kind: TriggerKindRerun},
		{name: "unknown kind has no additions", kind: "totally-made-up"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := envAdditionsForTriggerKind(tc.kind)
			if tc.wantKey == "" {
				if len(got) != 0 {
					t.Fatalf("expected no env additions, got %v", got)
				}
				return
			}
			if v, ok := got[tc.wantKey]; !ok || v != tc.wantVal {
				t.Fatalf("expected %s=%s, got %v", tc.wantKey, tc.wantVal, got)
			}
		})
	}
}

func TestCountPublishedTranscripts(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		want     int
	}{
		{name: "absent manifest is treated as zero", manifest: "", want: 0},
		{name: "empty files block is zero", manifest: `{"files":{}}`, want: 0},
		{name: "singular transcript counts as one", manifest: `{"files":{"transcript":"transcript.words.v1.json"}}`, want: 1},
		{name: "transcripts array counts entries", manifest: `{"files":{"transcripts":[{"id":"parakeet"},{"id":"canary"}]}}`, want: 2},
		{name: "malformed json treated as zero", manifest: `{this is not json`, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.manifest != "" {
				if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(tc.manifest), 0o644); err != nil {
					t.Fatalf("write manifest: %v", err)
				}
			}
			got, err := countPublishedTranscripts(dir)
			if err != nil {
				t.Fatalf("countPublishedTranscripts: %v", err)
			}
			if got != tc.want {
				t.Fatalf("count = %d, want %d (manifest=%q)", got, tc.want, tc.manifest)
			}
		})
	}
}

func TestJobEligibleForBackfill(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// No meeting path → not eligible, no error.
	job := Job{}
	eligible, reason, err := jobEligibleForBackfill(job)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if eligible {
		t.Fatalf("expected ineligible when meeting path is empty")
	}
	if reason == "" {
		t.Fatalf("expected non-empty reason when ineligible")
	}

	// Single-transcript bundle → eligible.
	mp := dir
	mustWrite("manifest.json", `{"files":{"transcript":"transcript.words.v1.json"}}`)
	mpStr := mp
	job = Job{ArtifactMeetingPath: &mpStr}
	eligible, _, err = jobEligibleForBackfill(job)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !eligible {
		t.Fatalf("expected single-transcript bundle to be eligible")
	}

	// Two-transcript bundle → not eligible.
	mustWrite("manifest.json", `{"files":{"transcripts":[{"id":"a"},{"id":"b"}]}}`)
	eligible, reason, err = jobEligibleForBackfill(job)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if eligible {
		t.Fatalf("expected two-transcript bundle to be ineligible")
	}
	if !strings.Contains(reason, "already publishes") {
		t.Fatalf("expected reason to mention 'already publishes', got %q", reason)
	}
}

func TestBackfillTranscriptsEndpointReturns404ForUnknownJob(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	srv := httptest.NewServer(newHTTPHandler(rt.logger, rt))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/jobs/nonexistent/backfill-transcripts", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestBackfillTranscriptsEndpointReturns409WhenAlreadyMultiTranscript(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	// Hand-build a completed job whose meeting bundle already publishes 2 transcripts.
	jobID := "job-already-multi"
	meetingPath := filepath.Join(rt.cfg.WorkRoot, "manual-meeting")
	if err := os.MkdirAll(meetingPath, 0o755); err != nil {
		t.Fatalf("mkdir meeting: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meetingPath, "manifest.json"), []byte(`{"files":{"transcripts":[{"id":"parakeet"},{"id":"canary"}]}}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	runPath := filepath.Join(rt.cfg.WorkRoot, "manual-run")
	if err := os.MkdirAll(runPath, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	if err := insertCompletedJobRow(rt, jobID, runPath, meetingPath); err != nil {
		t.Fatalf("seed job row: %v", err)
	}

	srv := httptest.NewServer(newHTTPHandler(rt.logger, rt))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/jobs/"+jobID+"/backfill-transcripts", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestBackfillEligibleEndpointListsSingleTranscriptJobs(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	// One eligible (v1-style) job.
	eligibleMeeting := filepath.Join(rt.cfg.WorkRoot, "eligible-meeting")
	if err := os.MkdirAll(eligibleMeeting, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(eligibleMeeting, "manifest.json"), []byte(`{"files":{"transcript":"transcript.words.v1.json"}}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := insertCompletedJobRow(rt, "job-eligible", filepath.Join(rt.cfg.WorkRoot, "eligible-run"), eligibleMeeting); err != nil {
		t.Fatalf("seed job row: %v", err)
	}

	// One ineligible (already multi) job.
	multiMeeting := filepath.Join(rt.cfg.WorkRoot, "multi-meeting")
	if err := os.MkdirAll(multiMeeting, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(multiMeeting, "manifest.json"), []byte(`{"files":{"transcripts":[{"id":"a"},{"id":"b"}]}}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := insertCompletedJobRow(rt, "job-multi", filepath.Join(rt.cfg.WorkRoot, "multi-run"), multiMeeting); err != nil {
		t.Fatalf("seed job row: %v", err)
	}

	srv := httptest.NewServer(newHTTPHandler(rt.logger, rt))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/backfill-transcripts/eligible")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body backfillEligibleResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Jobs) != 1 {
		t.Fatalf("expected 1 eligible job, got %d: %+v", len(body.Jobs), body.Jobs)
	}
	if body.Jobs[0].ID != "job-eligible" {
		t.Fatalf("expected eligible job to be job-eligible, got %+v", body.Jobs[0])
	}
	if body.Jobs[0].CurrentAttemptCount != 1 {
		t.Fatalf("expected CurrentAttemptCount=1, got %d", body.Jobs[0].CurrentAttemptCount)
	}
}

// insertCompletedJobRow seeds a minimal jobs+job_attempts row pair so that
// the backfill endpoints have something to operate on without going through
// the full record/build/publish dance.
func insertCompletedJobRow(rt *Runtime, jobID, runPath, meetingPath string) error {
	now := nowUTCString()
	_, err := rt.store.db.Exec(`
INSERT INTO jobs (
  id, provider, request_json,
  stage, state,
  current_attempt_number, rerun_count,
  artifact_run_path, artifact_meeting_path,
  created_at, updated_at,
  build_finished_at, completed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		jobID, "nextcloud-talk", `{}`,
		"done", "succeeded",
		1, 0,
		runPath, meetingPath,
		now, now,
		now, now,
	)
	if err != nil {
		return fmt.Errorf("insert job: %w", err)
	}
	_, err = rt.store.db.Exec(`
INSERT INTO job_attempts (
  job_id, attempt_number, trigger_kind, request_json,
  stage, state,
  artifact_run_path, artifact_meeting_path,
  created_at, updated_at, build_finished_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		jobID, 1, TriggerKindInitial, `{}`,
		"done", "succeeded",
		runPath, meetingPath,
		now, now, now,
	)
	if err != nil {
		return fmt.Errorf("insert attempt: %w", err)
	}
	return nil
}
