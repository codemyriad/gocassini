package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func modelTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	originalMemory := probeAvailableMem
	probeAvailableMem = func() int { return 1 << 20 }
	t.Cleanup(func() { probeAvailableMem = originalMemory })
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt := &Runtime{ctx: ctx, cancel: cancel, store: store, logger: log.New(io.Discard, "", 0), settingsPath: filepath.Join(root, "settings.json"), cfg: Config{ModelCacheRoot: root, CassiniBin: filepath.Join(root, "cassini"), DBPath: filepath.Join(root, "jobs.sqlite3")}}
	rt.setSettings(STTSettings{Quality: sttQualityFast, DeviceOverride: "cpu"})
	script := `#!/bin/sh
set -eu
case "$2" in
 list) cat "$CASSINI_CACHE_ROOT/inventory.json" ;;
 install)
  printf '%s\n' '{"version":1,"phase":"downloading","completed_bytes":5,"total_bytes":10}'
  if [ -e "$CASSINI_CACHE_ROOT/wait" ]; then sleep 30; fi
  printf '%s\n' '{"version":1,"phase":"installed","completed_bytes":10,"total_bytes":10}' ;;
 probe)
  printf '%s\n' '{"version":1,"phase":"checking"}'
  if [ -e "$CASSINI_CACHE_ROOT/fail-probe" ]; then echo 'probe refused' >&2; exit 1; fi
  printf '%s\n' '{"version":1,"phase":"ready"}' ;;
 *) exit 2 ;;
esac
`
	if err := os.WriteFile(rt.cfg.CassiniBin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	writeModelInventory(t, rt, false, false)
	t.Cleanup(func() { cancel(); rt.workerWG.Wait(); store.Close() })
	return rt
}
func writeModelInventory(t *testing.T, rt *Runtime, installed, ready bool) modelInfo {
	t.Helper()
	m := modelInfo{ID: modelParakeet110M, Revision: strings.Repeat("a", 64), Installed: installed, Ready: ready, Device: "cpu"}
	b, _ := json.Marshal([]modelInfo{m})
	if err := os.WriteFile(filepath.Join(rt.cfg.ModelCacheRoot, "inventory.json"), b, 0644); err != nil {
		t.Fatal(err)
	}
	return m
}
func modelRequest(rt *Runtime, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rt.modelsHandler(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}
func awaitModelState(t *testing.T, rt *Runtime, want string) modelJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		jobs, err := rt.modelJobs(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) == 1 && jobs[0].State == want {
			return jobs[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	jobs, _ := rt.modelJobs(context.Background())
	t.Fatalf("want %s; jobs=%+v", want, jobs)
	return modelJob{}
}
func TestModelRoutesUnderBothMountsAndDuplicateInstall(t *testing.T) {
	for _, base := range []string{"/", "/operator"} {
		t.Run(base, func(t *testing.T) {
			rt := modelTestRuntime(t)
			rt.cfg.BasePath = base
			api := http.NewServeMux()
			api.HandleFunc("/settings/models", rt.modelsHandler)
			api.HandleFunc("/settings/models/", rt.modelsHandler)
			root := http.NewServeMux()
			mountBasePathOnto(root, base, api, []string{"/settings/models", "/settings/models/"})
			rec := httptest.NewRecorder()
			root.ServeHTTP(rec, httptest.NewRequest("GET", strings.TrimRight(base, "/")+"/settings/models?device=cpu", nil))
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "downloads_allowed") {
				t.Fatalf("inventory: %d %s", rec.Code, rec.Body.String())
			}
			for i := 0; i < 2; i++ {
				rec = httptest.NewRecorder()
				root.ServeHTTP(rec, httptest.NewRequest("POST", strings.TrimRight(base, "/")+"/settings/models/install", strings.NewReader(fmt.Sprintf(`{"model":%q,"device":"cpu"}`, modelParakeet110M))))
				if rec.Code != 202 {
					t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
				}
				if strings.HasPrefix(rec.Header().Get("Location"), "//") {
					t.Fatal("invalid Location")
				}
			}
			jobs, err := rt.modelJobs(rt.ctx)
			if err != nil || len(jobs) != 1 {
				t.Fatalf("duplicates: %v %v", jobs, err)
			}
			if rt.currentSettings().TranscriptionEnabled {
				t.Fatal("installation enabled transcription")
			}
		})
	}
}
func TestModelPolicyAndActivation(t *testing.T) {
	rt := modelTestRuntime(t)
	rt.cfg.DisallowModelDownload = true
	rec := modelRequest(rt, "POST", "/settings/models/install", fmt.Sprintf(`{"model":%q,"device":"cpu"}`, modelParakeet110M))
	if rec.Code != 403 {
		t.Fatalf("airgap download accepted: %d", rec.Code)
	}
	m := writeModelInventory(t, rt, true, false)
	update := fmt.Sprintf(`{"quality":"fast","transcription_enabled":true,"active_model":%q,"active_revision":%q,"device_override":"cpu"}`, m.ID, m.Revision)
	rec = httptest.NewRecorder()
	rt.handlePutSettings(rec, httptest.NewRequest("PUT", "/settings", strings.NewReader(update)))
	if rec.Code != 409 || rt.currentSettings().TranscriptionEnabled {
		t.Fatalf("unready activation: %d %s", rec.Code, rec.Body.String())
	}
	writeModelInventory(t, rt, true, true)
	rec = httptest.NewRecorder()
	rt.handlePutSettings(rec, httptest.NewRequest("PUT", "/settings", strings.NewReader(update)))
	if rec.Code != 200 || !rt.currentSettings().TranscriptionEnabled {
		t.Fatalf("offline activation: %d %s", rec.Code, rec.Body.String())
	}
	writeModelInventory(t, rt, false, false)
	rec = httptest.NewRecorder()
	rt.handlePutSettings(rec, httptest.NewRequest("PUT", "/settings", strings.NewReader(`{"quality":"fast","transcription_enabled":false}`)))
	if rec.Code != 200 || rt.currentSettings().TranscriptionEnabled {
		t.Fatal("cannot disable with unavailable model")
	}
}
func TestModelCancelRetryAndRecovery(t *testing.T) {
	rt := modelTestRuntime(t)
	m := writeModelInventory(t, rt, false, false)
	if err := os.WriteFile(filepath.Join(rt.cfg.ModelCacheRoot, "wait"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	job, err := rt.enqueueModel(rt.ctx, m, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an installation interrupted by a previous operator process.
	job.State = "unpacking"
	if err := rt.updateModelJob(job); err != nil {
		t.Fatal(err)
	}
	rt.startModelWorker()
	active := awaitModelState(t, rt, "downloading")
	if active.Progress.Completed != 5 {
		t.Fatalf("progress not persisted: %+v", active)
	}
	rec := modelRequest(rt, "POST", "/settings/models/jobs/"+job.ID+"/cancel", "")
	if rec.Code != 202 {
		t.Fatalf("cancel: %d", rec.Code)
	}
	awaitModelState(t, rt, "cancelled")
	os.Remove(filepath.Join(rt.cfg.ModelCacheRoot, "wait"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec = modelRequest(rt, "POST", "/settings/models/jobs/"+job.ID+"/retry", "")
		if rec.Code == 202 {
			break
		}
		if rec.Code != 409 || time.Now().After(deadline) {
			t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	awaitModelState(t, rt, "ready")
	if rt.currentSettings().TranscriptionEnabled {
		t.Fatal("completion activated transcription")
	}
}
func TestModelProbeFailureIsNotReady(t *testing.T) {
	rt := modelTestRuntime(t)
	m := writeModelInventory(t, rt, true, false)
	if err := os.WriteFile(filepath.Join(rt.cfg.ModelCacheRoot, "fail-probe"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	j, err := rt.enqueueModel(rt.ctx, m, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	rt.runModelJob(j)
	failed := awaitModelState(t, rt, "failed")
	if !strings.Contains(failed.Error, "probe refused") {
		t.Fatalf("lost error: %+v", failed)
	}
}
