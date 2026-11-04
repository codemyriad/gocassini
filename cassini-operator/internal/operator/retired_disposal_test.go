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
	return insertPinnedDisposalJob(t, rt, id, v3Int8, retiredV3Rev, deferrals)
}

// insertPinnedDisposalJob queues the build of a delete-after-processing job whose
// saved transcription snapshot pins model at revision.
func insertPinnedDisposalJob(t *testing.T, rt *Runtime, id, model, revision string, deferrals int) (Job, string) {
	t.Helper()
	insertJob(t, rt.store.db, id, nowUTCString())
	snapshot := STTSettings{MeetingFormat: "json", SourceRetention: sourceDeleteAfterProcessing, TranscriptionEnabled: true, ActiveModel: model, ActiveRevision: revision, Quality: sttQualityBalanced, DeviceOverride: deviceCPU, Source: sttSourceUser}
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
	_, _, err := rt.admitTranscription(rt.ctx, snapshot, resourceLimitsFromEnv(), true)
	var wait *resourceUnavailableError
	if !errors.As(err, &wait) || wait.permanent || !wait.awaitsModel || !strings.Contains(err.Error(), "downloading, 50%") {
		t.Fatalf("admission while installing: %v", err)
	}
	onlyModelJob(t, rt)

	// Installed and checked: the build runs Ultra at its own revision, and
	// the child gets that model, not the retired one.
	setUltraInt8(t, rt, true, true)
	device, admitted, err := rt.admitTranscription(rt.ctx, snapshot, resourceLimitsFromEnv(), true)
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
	_, _, err := rt.admitTranscription(rt.ctx, snapshot, resourceLimitsFromEnv(), true)
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
	_, _, err = rt.admitTranscription(rt.ctx, snapshot, resourceLimitsFromEnv(), true)
	if !transientResourceError(err) || !strings.Contains(err.Error(), "ended failed (disk full)") {
		t.Fatalf("failed install: %v", err)
	}
	if j := onlyModelJob(t, rt); j.State != "failed" {
		t.Fatalf("admission restarted a failed install: %+v", j)
	}

	// A job that finished but whose files are gone runs again.
	job.State, job.Error = "ready", ""
	if err := rt.updateModelJob(job); err != nil {
		t.Fatal(err)
	}
	_, _, err = rt.admitTranscription(rt.ctx, snapshot, resourceLimitsFromEnv(), true)
	if !transientResourceError(err) || !strings.Contains(err.Error(), "being installed (queued)") {
		t.Fatalf("stale ready install: %v", err)
	}
	if j := onlyModelJob(t, rt); j.State != "queued" {
		t.Fatalf("stale ready install not requeued: %+v", j)
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

// A delete-after-processing job recorded after the upgrade saved the Ultra
// pin. When Ultra is not installed and nothing is installing it (downloads are
// disabled, or its install failed), the build waits for the model from its
// first attempt rather than failing: a failed disposal job has its source
// media deleted with no transcript.
func TestDisposalJobPinnedToAMissingModelWaitsAndKeepsItsMedia(t *testing.T) {
	for _, tc := range []struct {
		name, detail, jobState string
		setup                  func(t *testing.T, rt *Runtime)
	}{
		{name: "downloads disabled", detail: "is not installed; downloads are disabled, so import it with cassini models import", setup: func(t *testing.T, rt *Runtime) {
			rt.cfg.DisallowModelDownload = true
		}},
		{name: "install failed", detail: "is not installed; its install ended failed (network down); retry it in Settings", jobState: "failed", setup: func(t *testing.T, rt *Runtime) {
			job, err := rt.enqueueModel(rt.ctx, modelInfo{ID: modelParakeetUltraInt8, Revision: ultraInt8Rev}, deviceCPU)
			if err != nil {
				t.Fatal(err)
			}
			job.State, job.Error = "failed", "network down"
			if err := rt.updateModelJob(job); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "no install job", detail: "is not installed; being installed (queued)", jobState: "queued", setup: func(*testing.T, *Runtime) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := modelTestRuntime(t)
			cpuOnly(t)
			rt.cfg.WorkRoot = filepath.Join(t.TempDir(), "jobs")
			setUltraInt8(t, rt, false, false)
			rt.setSettings(STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetUltraInt8, ActiveRevision: ultraInt8Rev, Quality: sttQualityBalanced, DeviceOverride: deviceCPU, Source: sttSourceUser})
			tc.setup(t, rt)
			job, run := insertPinnedDisposalJob(t, rt, "dispose-ultra", modelParakeetUltraInt8, ultraInt8Rev, 0)
			rt.buildJobFn = rt.executeBuildCLI

			rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run}, 1)

			job = mustGetJob(t, rt.store, job.ID)
			if job.Stage != "build" || job.State != "queued" {
				t.Fatalf("job %s/%s (%v), want build/queued (deferred)", job.Stage, job.State, job.Error)
			}
			if job.Error == nil || !strings.Contains(*job.Error, "speech model "+modelParakeetUltraInt8+" unavailable: "+tc.detail) {
				t.Fatalf("deferral detail %v", job.Error)
			}
			jobs, err := rt.modelJobs(rt.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if tc.jobState == "" && len(jobs) != 0 {
				t.Fatalf("queued a download with downloads disabled: %+v", jobs)
			}
			if tc.jobState != "" && (len(jobs) != 1 || jobs[0].State != tc.jobState) {
				t.Fatalf("install jobs %+v, want one %s", jobs, tc.jobState)
			}
			rt.attemptMediaCleanup(job.ID)
			if _, err := os.Stat(run); err != nil {
				t.Fatalf("source media gone while waiting for the model: %v", err)
			}

			// At the ceiling it still waits.
			limit := rt.buildResourceDeferralLimit()
			rt.runBuildJob(buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run, DeferralCount: limit}, 1)
			if job = mustGetJob(t, rt.store, job.ID); job.Stage != "build" || job.State != "queued" {
				t.Fatalf("job at the ceiling %s/%s, want build/queued", job.Stage, job.State)
			}
		})
	}
}

// An ordinary job keeps its audio at once when its model is not installed and
// nothing is installing it: it queues no download of its own.
func TestOrdinaryJobDoesNotWaitForAMissingModel(t *testing.T) {
	rt := modelTestRuntime(t)
	cpuOnly(t)
	setUltraInt8(t, rt, false, false)
	settings := STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetUltraInt8, ActiveRevision: ultraInt8Rev, Quality: sttQualityBalanced, DeviceOverride: deviceCPU}
	_, _, err := rt.admitTranscription(rt.ctx, settings, resourceLimitsFromEnv(), false)
	if err == nil || transientResourceError(err) || !strings.Contains(err.Error(), "is not installed") {
		t.Fatalf("missing model: %v", err)
	}
	if jobs, _ := rt.modelJobs(rt.ctx); len(jobs) != 0 {
		t.Fatalf("an ordinary build queued an install: %+v", jobs)
	}
}

// An install that reaches "ready" between admission's inventory read and its
// look at the install jobs is not mistaken for a missing model: admission reads
// the inventory again and runs the build, without running the install again.
func TestAdmissionRereadsTheInventoryWhenTheInstallJustFinished(t *testing.T) {
	for _, dispose := range []bool{false, true} {
		rt := modelTestRuntime(t)
		cpuOnly(t)
		setUltraInt8(t, rt, true, true)
		installed, err := os.ReadFile(filepath.Join(rt.cfg.ModelCacheRoot, "inventory.json"))
		if err != nil {
			t.Fatal(err)
		}
		setUltraInt8(t, rt, false, false)
		// The first listing predates the install; later ones see it.
		if err := os.WriteFile(filepath.Join(rt.cfg.ModelCacheRoot, "inventory-after.json"), installed, 0o644); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\nset -eu\nr=\"$CASSINI_CACHE_ROOT\"\nif [ -e \"$r/listed\" ]; then cat \"$r/inventory-after.json\"; else : > \"$r/listed\"; cat \"$r/inventory.json\"; fi\n"
		if err := os.WriteFile(rt.cfg.CassiniBin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		job, err := rt.enqueueModel(rt.ctx, modelInfo{ID: modelParakeetUltraInt8, Revision: ultraInt8Rev}, deviceCPU)
		if err != nil {
			t.Fatal(err)
		}
		job.State = "ready"
		if err := rt.updateModelJob(job); err != nil {
			t.Fatal(err)
		}
		settings := STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetUltraInt8, ActiveRevision: ultraInt8Rev, Quality: sttQualityBalanced, DeviceOverride: deviceCPU}
		device, admitted, err := rt.admitTranscription(rt.ctx, settings, resourceLimitsFromEnv(), dispose)
		if err != nil || device != deviceCPU || admitted.ActiveModel != modelParakeetUltraInt8 {
			t.Fatalf("dispose=%v: admission after the install finished: %s %+v %v", dispose, device, admitted, err)
		}
		if j := onlyModelJob(t, rt); j.State != "ready" {
			t.Fatalf("dispose=%v: finished install ran again: %+v", dispose, j)
		}
	}
}
