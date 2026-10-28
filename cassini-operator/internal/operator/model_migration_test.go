package operator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	v3Int8          = "parakeet-tdt-0.6b-v3-int8"
	ultraInt8Rev    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	retiredV3Rev    = "9999999999999999999999999999999999999999999999999999999999999999"
	ultraFp32Rev    = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	installJobTable = "model_install_jobs"
)

func cpuOnly(t *testing.T) {
	t.Helper()
	t.Setenv(envSTTCUDACapable, "0")
	stubNVIDIADevice(t, false)
}

// setUltraInt8 rewrites the inventory with Ultra int8 installed and ready (or
// not), as `cassini models list` reports it after (or before) the install.
func setUltraInt8(t *testing.T, rt *Runtime, installed, ready bool) {
	t.Helper()
	models := []modelInfo{
		{ID: modelParakeet110M, Revision: strings.Repeat("a", 64), Device: deviceCPU, Kind: "speech", Installed: true, Ready: true},
		{ID: modelParakeetUltraInt8, Revision: ultraInt8Rev, Device: deviceCPU, Kind: "speech", Installed: installed, Ready: ready, Replaces: []string{v3Int8}},
		{ID: modelParakeetUltraFp32, Revision: ultraFp32Rev, Device: deviceCPU, Kind: "speech", Replaces: []string{"parakeet-tdt-0.6b-v3"}},
	}
	b, _ := json.Marshal(models)
	if err := os.WriteFile(filepath.Join(rt.cfg.ModelCacheRoot, "inventory.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	rt.invalidateModelInventory()
}

func onlyModelJob(t *testing.T, rt *Runtime) modelJob {
	t.Helper()
	jobs, err := rt.modelJobs(rt.ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("install jobs %+v %v, want exactly one", jobs, err)
	}
	return jobs[0]
}

// The install is queued before the moved pin is saved. If queueing fails (or
// the process dies) in between, the pin still names v3, so the next attempt
// migrates again instead of finding the pin moved and nothing installing it.
func TestRetiredPinMigrationQueuesTheInstallBeforeSavingThePin(t *testing.T) {
	rt := modelTestRuntime(t)
	cpuOnly(t)
	setUltraInt8(t, rt, false, false)
	pinned := STTSettings{TranscriptionEnabled: true, ActiveModel: v3Int8, ActiveRevision: retiredV3Rev, Quality: sttQualityBalanced, Source: sttSourceUser}
	if err := Save(rt.settingsPath, pinned); err != nil {
		t.Fatal(err)
	}
	rt.setSettings(pinned)

	// The job table cannot be written: queueing fails.
	if _, err := rt.store.db.Exec(`ALTER TABLE ` + installJobTable + ` RENAME TO held_model_install_jobs`); err != nil {
		t.Fatal(err)
	}
	migrated, err := rt.migrateRetiredSpeechModel(rt.ctx)
	if migrated || err == nil {
		t.Fatalf("migrated=%v err=%v, want an error and no move", migrated, err)
	}
	if got := rt.currentSettings(); got.ActiveModel != v3Int8 {
		t.Fatalf("pin moved without an install: %+v", got)
	}
	if onDisk, err := LoadOrInitSettings(rt.settingsPath); err != nil || onDisk.ActiveModel != v3Int8 {
		t.Fatalf("pin saved without an install: %+v %v", onDisk, err)
	}
	if _, err := rt.store.db.Exec(`ALTER TABLE held_model_install_jobs RENAME TO ` + installJobTable); err != nil {
		t.Fatal(err)
	}

	// The retry moves the pin and queues the install.
	migrated, err = rt.migrateRetiredSpeechModel(rt.ctx)
	if !migrated || err != nil {
		t.Fatalf("retry migrated=%v err=%v", migrated, err)
	}
	if got := rt.currentSettings(); got.ActiveModel != modelParakeetUltraInt8 || got.ActiveRevision != ultraInt8Rev {
		t.Fatalf("settings %+v", got)
	}
	if j := onlyModelJob(t, rt); j.Model != modelParakeetUltraInt8 || j.Revision != ultraInt8Rev || j.State != "queued" {
		t.Fatalf("job %+v", j)
	}
}

// A process that saved the Ultra pin and died before its install job was
// durable (the order an earlier build of this change used) left the active
// model missing with nothing installing it. Every start now queues the
// active model of enabled transcription when it is not installed and no
// install is running, and runs a finished job of it again.
func TestStartupQueuesAMissingActiveModel(t *testing.T) {
	saved := STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetUltraInt8, ActiveRevision: ultraInt8Rev, Quality: sttQualityBalanced, Source: sttSourceUser}

	t.Run("saved pin, no job", func(t *testing.T) {
		rt := modelTestRuntime(t)
		cpuOnly(t)
		setUltraInt8(t, rt, false, false)
		rt.setSettings(saved)
		if migrated, err := rt.migrateRetiredSpeechModel(rt.ctx); migrated || err != nil {
			t.Fatalf("already-moved pin: migrated=%v err=%v", migrated, err)
		}
		if err := rt.recoverActiveSpeechModel(rt.ctx); err != nil {
			t.Fatal(err)
		}
		if j := onlyModelJob(t, rt); j.Model != modelParakeetUltraInt8 || j.Revision != ultraInt8Rev || j.Device != deviceCPU || j.State != "queued" {
			t.Fatalf("job %+v", j)
		}
		// Idempotent while that job is queued.
		if err := rt.recoverActiveSpeechModel(rt.ctx); err != nil {
			t.Fatal(err)
		}
		onlyModelJob(t, rt)
	})

	t.Run("failed job runs again", func(t *testing.T) {
		rt := modelTestRuntime(t)
		cpuOnly(t)
		setUltraInt8(t, rt, false, false)
		rt.setSettings(saved)
		job, err := rt.enqueueModel(rt.ctx, modelInfo{ID: modelParakeetUltraInt8, Revision: ultraInt8Rev}, deviceCPU)
		if err != nil {
			t.Fatal(err)
		}
		job.State, job.Error = "failed", "connection reset"
		if err := rt.updateModelJob(job); err != nil {
			t.Fatal(err)
		}
		if err := rt.recoverActiveSpeechModel(rt.ctx); err != nil {
			t.Fatal(err)
		}
		if j := onlyModelJob(t, rt); j.State != "queued" || j.Error != "" {
			t.Fatalf("job %+v", j)
		}
	})

	t.Run("downloads disabled, transcription off", func(t *testing.T) {
		rt := modelTestRuntime(t)
		cpuOnly(t)
		setUltraInt8(t, rt, false, false)
		rt.cfg.DisallowModelDownload = true
		rt.setSettings(saved)
		if err := rt.recoverActiveSpeechModel(rt.ctx); err != nil {
			t.Fatal(err)
		}
		rt.cfg.DisallowModelDownload = false
		off := saved
		off.TranscriptionEnabled = false
		rt.setSettings(off)
		if err := rt.recoverActiveSpeechModel(rt.ctx); err != nil {
			t.Fatal(err)
		}
		if jobs, _ := rt.modelJobs(rt.ctx); len(jobs) != 0 {
			t.Fatalf("queued %+v", jobs)
		}
	})

	t.Run("worker retries until it can queue", func(t *testing.T) {
		rt := modelTestRuntime(t)
		cpuOnly(t)
		retry := speechModelCheckRetry
		speechModelCheckRetry = 50 * time.Millisecond
		t.Cleanup(func() { speechModelCheckRetry = retry })
		// No inventory yet: the first check fails.
		if err := os.Remove(filepath.Join(rt.cfg.ModelCacheRoot, "inventory.json")); err != nil {
			t.Fatal(err)
		}
		rt.setSettings(saved)
		rt.startModelWorker()
		time.Sleep(100 * time.Millisecond)
		if jobs, _ := rt.modelJobs(rt.ctx); len(jobs) != 0 {
			t.Fatalf("queued without an inventory: %+v", jobs)
		}
		setUltraInt8(t, rt, false, false)
		deadline := time.Now().Add(30 * time.Second)
		for {
			jobs, err := rt.modelJobs(rt.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) == 1 && jobs[0].Model == modelParakeetUltraInt8 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the retry never queued the install: %+v", jobs)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}
