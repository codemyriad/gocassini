package operator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// insertRetiredDisposalJob queues the build of a delete-after-processing job
// whose saved transcription snapshot pins Parakeet v3, as a job recorded
// before the upgrade has it.
func insertRetiredDisposalJob(t *testing.T, rt *Runtime, id string, deferrals int) (Job, string) {
	t.Helper()
	insertJob(t, rt.store.db, id, nowUTCString())
	snapshot := STTSettings{MeetingFormat: "json", SourceRetention: sourceDeleteAfterProcessing, TranscriptionEnabled: true, ActiveModel: v3Int8, ActiveRevision: retiredV3Rev, Quality: sttQualityBalanced, DeviceOverride: deviceCPU, Source: sttSourceUser}
	raw, _ := json.Marshal(TriggerRequest{CaptureMode: "audio-only", ProcessingPolicy: &recordingProcessingPolicy{MeetingFormat: "json", SourceRetention: sourceDeleteAfterProcessing, Transcription: &snapshot}})
	if _, err := rt.store.db.Exec(`UPDATE jobs SET request_json=? WHERE id=?`, string(raw), id); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec(`INSERT INTO media_cleanup(job_id) VALUES(?)`, id); err != nil {
		t.Fatal(err)
	}
	run := seedReadyRunBundle(t, rt.cfg.WorkRoot, id)
	if err := rt.store.MarkBuildQueued(context.Background(), id, run, run, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"jobs", "job_attempts"} {
		col := "id"
		if table == "job_attempts" {
			col = "job_id"
		}
		if _, err := rt.store.db.Exec(`UPDATE `+table+` SET build_deferral_count=? WHERE `+col+`=?`, deferrals, id); err != nil {
			t.Fatal(err)
		}
	}
	return mustGetJob(t, rt.store, id), run
}

// A delete-after-processing job saved its transcription settings when it was
// recorded, so the startup migration of the global settings does not reach a
// v3 pin in it. Its build resolves the pin to the model replacing it on its
// tier and device, at that model's revision, queues the install when nothing
// else has, and waits for it — even past the retry ceiling, where a failed
// disposal job would have its source media deleted with no transcript.
func TestDisposalJobPinnedToV3WaitsForUltraAndKeepsItsMedia(t *testing.T) {
	rt := modelTestRuntime(t)
	cpuOnly(t)
	rt.cfg.WorkRoot = filepath.Join(t.TempDir(), "jobs")
	setUltraInt8(t, rt, false, false)
	// The global settings moved on (fast tier, 110M installed), so nothing
	// but this job needs Ultra.
	rt.setSettings(STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeet110M, ActiveRevision: strings.Repeat("a", 64), Quality: sttQualityFast, DeviceOverride: deviceCPU, Source: sttSourceUser})
	limit := rt.buildResourceDeferralLimit()
	job, run := insertRetiredDisposalJob(t, rt, "dispose-v3", limit)
	rt.buildJobFn = rt.executeBuildCLI

	rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run, DeferralCount: limit}, 1)

	job = mustGetJob(t, rt.store, job.ID)
	if job.Stage != "build" || job.State != "queued" {
		t.Fatalf("job %s/%s, want build/queued (deferred)", job.Stage, job.State)
	}
	if job.Error == nil || !strings.Contains(*job.Error, "replaces retired "+v3Int8) {
		t.Fatalf("deferral detail %v", job.Error)
	}
	if j := onlyModelJob(t, rt); j.Model != modelParakeetUltraInt8 || j.Revision != ultraInt8Rev || j.Device != deviceCPU || j.State != "queued" {
		t.Fatalf("install job %+v", j)
	}
	rt.attemptMediaCleanup(job.ID)
	if _, err := os.Stat(run); err != nil {
		t.Fatalf("source media gone while waiting for the model: %v", err)
	}

	// Next round, the install is running: still waiting, no second job.
	attempt := onlyModelJob(t, rt)
	attempt.State, attempt.Progress = "downloading", modelProgress{Version: 1, Phase: "downloading", Completed: 1, Total: 2}
	if err := rt.updateModelJob(attempt); err != nil {
		t.Fatal(err)
	}
	snapshot := *processingPolicy(job).Transcription
	_, _, err := rt.admitTranscription(rt.ctx, snapshot, resourceLimitsFromEnv())
	var wait *resourceUnavailableError
	if !errors.As(err, &wait) || wait.permanent || !wait.awaitsModel || !strings.Contains(err.Error(), "downloading, 50%") {
		t.Fatalf("admission while installing: %v", err)
	}
	onlyModelJob(t, rt)

	// Installed and checked: the build runs Ultra at its own revision, and
	// the child gets that model, not the retired one.
	setUltraInt8(t, rt, true, true)
	device, admitted, err := rt.admitTranscription(rt.ctx, snapshot, resourceLimitsFromEnv())
	if err != nil || device != deviceCPU || admitted.ActiveModel != modelParakeetUltraInt8 || admitted.ActiveRevision != ultraInt8Rev {
		t.Fatalf("admission once installed: %s %+v %v", device, admitted, err)
	}
	env := admitted.ChildEnv(nil)
	if v, _ := envValue(env, envSTTModel); v != modelParakeetUltraInt8 {
		t.Fatalf("child model %q", v)
	}
	if v, _ := envValue(env, "CASSINI_STT_REVISION"); v != ultraInt8Rev {
		t.Fatalf("child revision %q", v)
	}
}

// With downloads disabled the replacement is not fetched; the job waits for
// an imported model, and a failed or cancelled install is reported rather
// than restarted on every deferral.
func TestDisposalJobPinnedToV3WithoutADownload(t *testing.T) {
	rt := modelTestRuntime(t)
	cpuOnly(t)
	setUltraInt8(t, rt, false, false)
	snapshot := STTSettings{MeetingFormat: "json", SourceRetention: sourceDeleteAfterProcessing, TranscriptionEnabled: true, ActiveModel: v3Int8, ActiveRevision: retiredV3Rev, Quality: sttQualityBalanced, DeviceOverride: deviceCPU}

	rt.cfg.DisallowModelDownload = true
	_, _, err := rt.admitTranscription(rt.ctx, snapshot, resourceLimitsFromEnv())
	if !transientResourceError(err) || !strings.Contains(err.Error(), "cassini models import") {
		t.Fatalf("downloads disabled: %v", err)
	}
	if jobs, _ := rt.modelJobs(rt.ctx); len(jobs) != 0 {
		t.Fatalf("queued a download with downloads disabled: %+v", jobs)
	}

	rt.cfg.DisallowModelDownload = false
	job, err := rt.enqueueModel(rt.ctx, modelInfo{ID: modelParakeetUltraInt8, Revision: ultraInt8Rev}, deviceCPU)
	if err != nil {
		t.Fatal(err)
	}
	job.State, job.Error = "failed", "disk full"
	if err := rt.updateModelJob(job); err != nil {
		t.Fatal(err)
	}
	_, _, err = rt.admitTranscription(rt.ctx, snapshot, resourceLimitsFromEnv())
	if !transientResourceError(err) || !strings.Contains(err.Error(), "ended failed (disk full)") {
		t.Fatalf("failed install: %v", err)
	}
	if j := onlyModelJob(t, rt); j.State != "failed" {
		t.Fatalf("admission restarted a failed install: %+v", j)
	}
}

// Past the ceiling, a disposal job still fails on any other resource
// condition: only waiting for its speech model is exempt.
func TestDisposalJobCeilingStillAppliesToOtherResources(t *testing.T) {
	rt := modelTestRuntime(t)
	rt.cfg.WorkRoot = filepath.Join(t.TempDir(), "jobs")
	limit := rt.buildResourceDeferralLimit()
	job, run := insertRetiredDisposalJob(t, rt, "dispose-ram", limit)
	rt.buildJobFn = func(context.Context, buildTask) (string, error) {
		return "", &resourceUnavailableError{resource: "memory", detail: "busy"}
	}
	rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run, DeferralCount: limit}, 1)
	if job = mustGetJob(t, rt.store, job.ID); job.Stage != "done" || job.State != "failed" {
		t.Fatalf("job %s/%s, want done/failed", job.Stage, job.State)
	}
}
