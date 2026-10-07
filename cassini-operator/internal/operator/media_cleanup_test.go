package operator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func insertDisposalJob(t *testing.T, rt *Runtime, id, stage, state string) Job {
	t.Helper()
	insertJob(t, rt.store.db, id, nowUTCString())
	settings := disposalSettings()
	raw, _ := json.Marshal(TriggerRequest{CaptureMode: "audio-only", ProcessingPolicy: &recordingProcessingPolicy{MeetingFormat: "json", SourceRetention: sourceDeleteAfterProcessing, Transcription: &settings}})
	if _, err := rt.store.db.Exec(`UPDATE jobs SET request_json=?,stage=?,state=? WHERE id=?`, string(raw), stage, state, id); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec(`UPDATE job_attempts SET stage=?,state=? WHERE job_id=?`, stage, state, id); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec(`INSERT INTO media_cleanup(job_id) VALUES(?)`, id); err != nil {
		t.Fatal(err)
	}
	return mustGetJob(t, rt.store, id)
}
func mediaFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
}
func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("still present %s: %v", path, err)
	}
}

func TestMediaCleanupDeletesEveryCopyAndPreservesText(t *testing.T) {
	for _, state := range []string{"succeeded", "failed", "interrupted"} {
		t.Run(state, func(t *testing.T) {
			rt, close := newBareSealRuntime(t)
			defer close()
			job := insertDisposalJob(t, rt, "cleanup", "done", state)
			root := rt.cfg.WorkRoot
			paths := []string{filepath.Join(canonicalRunPath(root, job.ID), "session", "audio.rtp"), filepath.Join(attemptRunPath(root, job.ID, 1), "recording.mkv"), filepath.Join(canonicalMeetingPath(root, job.ID), "meeting.webm"), filepath.Join(attemptMeetingPath(root, job.ID, 1), "meeting.webm"), canonicalOpusPath(root, job.ID), filepath.Join(attemptSitePath(root, job.ID, 1), "partial.opus"), filepath.Join(attemptScratchPath(root, job.ID, 1), "mix", "decoded.wav"), filepath.Join(attemptSealDir(root, job.ID, 1), ".transcription-leftover", "meeting.opus")}
			for _, p := range mediaPromotionPaths(root, job.ID) {
				paths = append(paths, filepath.Join(p, "leftover"))
			}
			for _, p := range paths {
				mediaFile(t, p)
			}
			// All aliases must disappear, not only one name of a retained inode.
			alias := attemptOpusPath(root, job.ID, 1)
			if err := os.Link(canonicalOpusPath(root, job.ID), alias); err != nil {
				t.Fatal(err)
			}
			paths = append(paths, alias)
			keep := []string{filepath.Join(currentRoot(root), job.ID+".json"), filepath.Join(attemptSealDir(root, job.ID, 1), job.ID+".json"), attemptLogPath(root, job.ID, 1, "build"), filepath.Join(root, "unrelated.run", "recording.mkv")}
			for _, p := range keep {
				mediaFile(t, p)
			}
			rt.attemptMediaCleanup(job.ID)
			for _, p := range paths {
				assertAbsent(t, p)
			}
			for _, p := range keep {
				if _, err := os.Stat(p); err != nil {
					t.Fatalf("removed retained file %s: %v", p, err)
				}
			}
			status := rt.mediaCleanupStatus(job)
			if status.Status != "completed" || status.CompletedAt == "" {
				t.Fatalf("%+v", status)
			}
			a := rt.artifactAvailability(job)
			if a.Source != "deleted" || a.RerunBlockedReason == "" {
				t.Fatalf("%+v", a)
			}
			rt.attemptMediaCleanup(job.ID) // idempotent
			if _, err := rt.store.QueueRerunAttempt(context.Background(), job, nowUTCString()); !errors.Is(err, ErrJobNotEligibleForRerun) {
				t.Fatalf("rerun: %v", err)
			}
		})
	}
}

func TestMediaCleanupPendingBlocksRerunButDoesNotTouchActiveJob(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	job := insertDisposalJob(t, rt, "active", "build", "queued")
	file := filepath.Join(canonicalRunPath(rt.cfg.WorkRoot, job.ID), "recording.mkv")
	mediaFile(t, file)
	rt.attemptMediaCleanup(job.ID)
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	if rt.mediaCleanupStatus(job).Status != "waiting" {
		t.Fatal("active job cleaned")
	}
	if _, err := rt.store.QueueRerunAttempt(context.Background(), job, nowUTCString()); !errors.Is(err, ErrJobNotEligibleForRerun) {
		t.Fatal(err)
	}
	rt.store.db.Exec(`UPDATE jobs SET stage='done',state='failed' WHERE id=?`, job.ID)
	job = mustGetJob(t, rt.store, job.ID)
	if rt.mediaCleanupStatus(job).Status != "pending" {
		t.Fatal("terminal commit lost cleanup obligation")
	}
	// Simulates startup recovery after termination without the stage defer running.
	rt.runMediaCleanupPass()
	assertAbsent(t, file)
}

func TestMediaCleanupFailureAndRetryDoesNotFollowSymlinks(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	job := insertDisposalJob(t, rt, "unsafe", "done", "failed")
	outside := filepath.Join(t.TempDir(), "keep.opus")
	mediaFile(t, outside)
	source := canonicalRunPath(rt.cfg.WorkRoot, job.ID)
	os.MkdirAll(source, 0700)
	link := filepath.Join(source, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	rt.attemptMediaCleanup(job.ID)
	status := rt.mediaCleanupStatus(job)
	if status.Status != "error" || status.LastError == "" {
		t.Fatalf("%+v", status)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("followed symlink")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	rt.jobDetailHandler(rec, httptest.NewRequest("POST", "/jobs/unsafe/cleanup", nil))
	if rec.Code != 202 {
		t.Fatalf("retry %d: %s", rec.Code, rec.Body.String())
	}
	rt.runMediaCleanupPass()
	if rt.mediaCleanupStatus(job).Status != "completed" {
		t.Fatal("retry did not finish")
	}
}

func TestMediaCleanupRecoversJournalBeforeReportingCompletion(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	job := insertDisposalJob(t, rt, "recovery", "done", "failed")
	path := canonicalRunPath(rt.cfg.WorkRoot, job.ID)
	mediaFile(t, filepath.Join(path, "recording.mkv"))
	dir := rt.operationDir(job.ID)
	os.MkdirAll(dir, 0700)
	rel, _ := filepath.Rel(rt.cfg.WorkRoot, path)
	op := artifactOperation{Job: job.ID, Attempt: 1, Action: "remove", Kind: "media-disposal", Targets: []string{rel}}
	if err := rt.saveOperation(op); err != nil {
		t.Fatal(err)
	}
	// Crash after rename, before unlink/database completion.
	if err := os.Rename(path, filepath.Join(dir, "old-0")); err != nil {
		t.Fatal(err)
	}
	if rt.mediaCleanupStatus(job).Status == "completed" {
		t.Fatal("premature success")
	}
	rt.runMediaCleanupPass()
	assertAbsent(t, dir)
	if rt.mediaCleanupStatus(job).Status != "completed" {
		t.Fatal("recovery incomplete")
	}
}

func TestMediaCleanupSkipsReservedJobsAndArchiveReaders(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	job := insertDisposalJob(t, rt, "reserved", "done", "failed")
	file := filepath.Join(canonicalRunPath(rt.cfg.WorkRoot, job.ID), "recording.mkv")
	mediaFile(t, file)
	unlock := rt.store.lockArtifacts(job.ID)
	rt.runMediaCleanupPass()
	unlock()
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	rt.store.artifactGate.RLock()
	rt.runMediaCleanupPass()
	rt.store.artifactGate.RUnlock()
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	rt.runMediaCleanupPass()
	assertAbsent(t, file)
}

func TestDisposalPromotesOnlyJSON(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	job := insertDisposalJob(t, rt, "json-only", "done", "succeeded")
	media := filepath.Join(attemptMeetingPath(rt.cfg.WorkRoot, job.ID, 1), "meeting.webm")
	mediaFile(t, media)
	seal := filepath.Join(attemptSealDir(rt.cfg.WorkRoot, job.ID, 1), job.ID+".json")
	mediaFile(t, seal)
	digest, _ := fileSHA256(seal)
	rt.store.db.Exec(`UPDATE job_attempts SET artifact_opus_path=?,artifact_opus_sha256=?,publish_finished_at=? WHERE job_id=?`, seal, digest, nowUTCString(), job.ID)
	if err := rt.promotePublishedPair(job.ID, 1); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, canonicalMeetingPath(rt.cfg.WorkRoot, job.ID))
	rt.attemptMediaCleanup(job.ID)
	assertAbsent(t, media)
	// Reconciliation cannot resurrect media or discard JSON.
	rt.reconcileArtifactDuplicatesOnStartup()
	if _, err := os.Stat(filepath.Join(currentRoot(rt.cfg.WorkRoot), job.ID+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestRequireCompletedTranscription(t *testing.T) {
	dir := t.TempDir()
	for _, status := range []string{"completed", "failed", "skipped", ""} {
		raw, _ := json.Marshal(map[string]any{"processing": map[string]any{"transcription": map[string]string{"status": status}}})
		os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600)
		if err := requireCompletedTranscription(dir); (err == nil) != (status == "completed") {
			t.Fatalf("%s: %v", status, err)
		}
	}
}

func TestTerminalStageFailuresRunMediaCleanup(t *testing.T) {
	for _, stage := range []string{"build", "seal", "publish"} {
		t.Run(stage, func(t *testing.T) {
			rt, close := newBareSealRuntime(t)
			defer close()
			job := insertDisposalJob(t, rt, "stage-fail", stage, "queued")
			media := filepath.Join(attemptMeetingPath(rt.cfg.WorkRoot, job.ID, 1), "meeting.webm")
			mediaFile(t, media)
			switch stage {
			case "build":
				run := seedReadyRunBundle(t, rt.cfg.WorkRoot, job.ID)
				if err := rt.store.MarkBuildQueued(context.Background(), job.ID, run, run, nowUTCString()); err != nil {
					t.Fatal(err)
				}
				rt.buildJobFn = func(context.Context, buildTask) (string, error) {
					return filepath.Dir(media), errors.New("build failed")
				}
				rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run}, 1)
			case "seal":
				rt.sealJobFn = func(context.Context, sealTask) (string, error) { return "", errors.New("seal failed") }
				rt.runSealJob(sealTask{JobID: job.ID, AttemptNumber: 1})
			case "publish":
				rt.publishJobFn = func(context.Context, publishTask) (string, error) { return "", errors.New("publish failed") }
				rt.runPublishJob(publishTask{JobID: job.ID, AttemptNumber: 1})
			}
			assertAbsent(t, media)
			if state := rt.mediaCleanupStatus(mustGetJob(t, rt.store, job.ID)); state.Status != "completed" {
				t.Fatalf("%+v", state)
			}
		})
	}
}

func TestMediaCleanupScratchIsJobOwned(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	job := insertDisposalJob(t, rt, "scratch", "build", "running")
	env, err := rt.mediaScratchEnv([]string{"TMPDIR=/unowned", "PATH=/bin"}, job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	tmp, _ := envValue(env, "TMPDIR")
	if tmp != attemptScratchPath(rt.cfg.WorkRoot, job.ID, 1) {
		t.Fatalf("scratch escaped job: %s", tmp)
	}
	if path, _ := envValue(env, "PATH"); path != "/bin" {
		t.Fatal("lost environment")
	}
}

func TestDisposalPermanentResourceFailureEndsJob(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	job := insertDisposalJob(t, rt, "blocked", "build", "queued")
	run := seedReadyRunBundle(t, rt.cfg.WorkRoot, job.ID)
	if err := rt.store.MarkBuildQueued(context.Background(), job.ID, run, run, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	rt.buildJobFn = func(context.Context, buildTask) (string, error) {
		return "", &resourceUnavailableError{resource: "model", detail: "unavailable", permanent: true}
	}
	rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run}, 1)
	job = mustGetJob(t, rt.store, job.ID)
	if job.State != "failed" || job.Stage != "done" {
		t.Fatalf("stranded media: %s/%s", job.Stage, job.State)
	}
	assertAbsent(t, run)
}
