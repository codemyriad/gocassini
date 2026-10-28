package operator

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	v3Int8Rev = "5793d0fd397c5778d2cf2126994d58e9d56b1be7c04d13c7a15bb1b4eafb16bf"
	v3Fp32Rev = "9125e6a7beb392821f19a3633b80bb2847599580e34ed42a4128732978ad759c"
)

// standInScript is the fake recorder of these tests: `models list` reads the
// inventory, `models probe` records the model it checked and fails for one
// that has a fail-probe-<id> file, `models install` reports a download, and
// `build` records its environment and writes a transcribed bundle.
const standInScript = `#!/bin/sh
set -eu
r="$CASSINI_CACHE_ROOT"
case "$1 $2" in
"models list") cat "$r/inventory.json" ;;
"models probe")
  echo "$3" >> "$r/probes"
  printf '%s\n' '{"version":1,"phase":"checking"}'
  if [ -e "$r/fail-probe-$3" ]; then echo 'probe refused' >&2; exit 1; fi
  printf '%s\n' '{"version":1,"phase":"ready"}' ;;
"models install")
  printf '%s\n' '{"version":1,"phase":"downloading","completed_bytes":5,"total_bytes":10}' ;;
build*)
  out=""; prev=""
  for a in "$@"; do [ "$prev" = "--out" ] && out="$a"; prev="$a"; done
  mkdir -p "$out"
  env | grep '^CASSINI_STT_' | sort > "$r/build-env"
  echo '{}' > "$out/cassini.json"
  echo '{"processing":{"transcription":{"status":"completed"}}}' > "$out/manifest.json" ;;
*) exit 2 ;;
esac
`

// standInRuntime is a model test runtime on the CPU whose inventory has
// Ultra int8 in the given state and, unless v3Installed is false, the retired
// v3 int8 files an upgraded install still has.
func standInRuntime(t *testing.T, ultraInstalled, ultraReady, v3Installed, v3Ready bool) *Runtime {
	t.Helper()
	rt := modelTestRuntime(t)
	cpuOnly(t)
	if err := os.WriteFile(rt.cfg.CassiniBin, []byte(standInScript), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStandInInventory(t, rt, deviceCPU, ultraInstalled, ultraReady, v3Installed, v3Ready)
	return rt
}

func writeStandInInventory(t *testing.T, rt *Runtime, device string, ultraInstalled, ultraReady, v3Installed, v3Ready bool) {
	t.Helper()
	models := []modelInfo{
		{ID: modelParakeet110M, Revision: strings.Repeat("a", 64), Device: device, Kind: "speech", Installed: true, Ready: true},
		{ID: modelParakeetUltraInt8, Revision: ultraInt8Rev, Device: device, Kind: "speech", Installed: ultraInstalled, Ready: ultraReady, Replaces: []string{v3Int8}},
		{ID: modelParakeetUltraFp32, Revision: ultraFp32Rev, Device: device, Kind: "speech", Installed: ultraInstalled, Ready: ultraReady, Replaces: []string{modelParakeetV3Fp32}},
	}
	if v3Installed {
		if device == deviceCPU {
			models = append(models, modelInfo{ID: v3Int8, Revision: v3Int8Rev, Device: device, Kind: "speech", Installed: true, Ready: v3Ready, ReplacedBy: modelParakeetUltraInt8})
		}
		models = append(models, modelInfo{ID: modelParakeetV3Fp32, Revision: v3Fp32Rev, Device: device, Kind: "speech", Installed: true, Ready: v3Ready, ReplacedBy: modelParakeetUltraFp32})
	}
	b, _ := json.Marshal(models)
	if err := os.WriteFile(filepath.Join(rt.cfg.ModelCacheRoot, "inventory.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	rt.invalidateModelInventory()
}

func probesRun(t *testing.T, rt *Runtime) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(rt.cfg.ModelCacheRoot, "probes"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(raw))
}

func ultraInt8Settings() STTSettings {
	return STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetUltraInt8, ActiveRevision: ultraInt8Rev, Quality: sttQualityBalanced, DeviceOverride: deviceCPU, Source: sttSourceUser}
}

// A retired model is only ever a stand-in: nothing selects it, installs it,
// checks it for a setting or shows it in Settings.
func TestRetiredRowsAreNotSelectable(t *testing.T) {
	rt := standInRuntime(t, true, true, true, true)
	inventory, err := rt.modelInventory(rt.ctx, deviceCPU)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := findModel(inventory, v3Int8, ""); err == nil {
		t.Fatal("findModel selected a retired model")
	}
	if _, retired, err := retiredReplacement(inventory, v3Int8, sttQualityBalanced, deviceCPU); !retired || err != nil {
		t.Fatalf("a v3 pin with v3 installed is not migrated: retired=%v %v", retired, err)
	}

	rec := modelRequest(rt, http.MethodPost, "/settings/models/install", `{"model":"`+v3Int8+`","revision":"`+v3Int8Rev+`","device":"cpu"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("install v3 = %d %s", rec.Code, rec.Body.String())
	}
	rec = modelRequest(rt, http.MethodGet, "/settings/models?device=cpu", "")
	var listed struct {
		Models []modelInfo `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("GET models = %d %s", rec.Code, rec.Body.String())
	}
	if len(listed.Models) != 3 {
		t.Fatalf("Settings lists %+v", listed.Models)
	}
	for _, m := range listed.Models {
		if m.ReplacedBy != "" {
			t.Fatalf("Settings lists retired %s", m.ID)
		}
	}

	rec = httptest.NewRecorder()
	rt.settingsHandler(rec, httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"quality":"balanced","transcription_enabled":true,"active_model":"`+v3Int8+`","active_revision":"`+v3Int8Rev+`","device_override":"cpu"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("PUT settings with v3 = %d %s", rec.Code, rec.Body.String())
	}
}

// The startup migration moves a v3 pin to Ultra and queues the download
// whether or not the v3 files are still there.
func TestRetiredPinMigratesWhileV3IsStillInstalled(t *testing.T) {
	rt := standInRuntime(t, false, false, true, true)
	pinned := STTSettings{TranscriptionEnabled: true, ActiveModel: v3Int8, ActiveRevision: v3Int8Rev, Quality: sttQualityBalanced, Source: sttSourceUser}
	if err := Save(rt.settingsPath, pinned); err != nil {
		t.Fatal(err)
	}
	rt.setSettings(pinned)
	if migrated, err := rt.migrateRetiredSpeechModel(rt.ctx); !migrated || err != nil {
		t.Fatalf("migrated=%v err=%v", migrated, err)
	}
	if got := rt.currentSettings(); got.ActiveModel != modelParakeetUltraInt8 || got.ActiveRevision != ultraInt8Rev {
		t.Fatalf("settings %+v", got)
	}
	if j := onlyModelJob(t, rt); j.Model != modelParakeetUltraInt8 || j.State != "queued" {
		t.Fatalf("install job %+v", j)
	}
	// recoverActiveSpeechModel looks at Ultra only and does not touch v3.
	if err := rt.recoverActiveSpeechModel(rt.ctx); err != nil {
		t.Fatal(err)
	}
	if probes := probesRun(t, rt); len(probes) != 0 {
		t.Fatalf("startup checked %v", probes)
	}
}

// While Ultra is not ready, a build transcribes with the v3 files the install
// still has, and the child is told it is a stand-in and why; Ultra's install
// is left as it was.
func TestBuildTranscribesWithV3WhileUltraDownloads(t *testing.T) {
	cases := []struct {
		name                string
		ultraInstalled      bool
		setup               func(t *testing.T, rt *Runtime)
		reason, detail, job string
	}{
		{name: "downloading", reason: "installing", detail: "being installed (downloading, 40%)", job: "downloading", setup: func(t *testing.T, rt *Runtime) {
			j, err := rt.enqueueModel(rt.ctx, modelInfo{ID: modelParakeetUltraInt8, Revision: ultraInt8Rev}, deviceCPU)
			if err != nil {
				t.Fatal(err)
			}
			j.State, j.Progress = "downloading", modelProgress{Version: 1, Phase: "downloading", Completed: 2, Total: 5}
			if err := rt.updateModelJob(j); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "install failed", reason: "install_failed", detail: "is not installed", job: "failed", setup: func(t *testing.T, rt *Runtime) {
			j, err := rt.enqueueModel(rt.ctx, modelInfo{ID: modelParakeetUltraInt8, Revision: ultraInt8Rev}, deviceCPU)
			if err != nil {
				t.Fatal(err)
			}
			j.State, j.Error = "failed", "network down"
			if err := rt.updateModelJob(j); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "downloads disabled", reason: "downloads_disabled", detail: "is not installed", setup: func(t *testing.T, rt *Runtime) {
			rt.cfg.DisallowModelDownload = true
		}},
		{name: "runtime check failed", ultraInstalled: true, reason: "runtime_check_failed", detail: "failed its runtime check: probe:", setup: func(t *testing.T, rt *Runtime) {
			if err := os.WriteFile(filepath.Join(rt.cfg.ModelCacheRoot, "fail-probe-"+modelParakeetUltraInt8), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt := standInRuntime(t, c.ultraInstalled, false, true, true)
			c.setup(t, rt)
			before, _ := rt.modelJobs(rt.ctx)

			device, admitted, err := rt.admitTranscription(rt.ctx, ultraInt8Settings(), resourceLimitsFromEnv(), false)
			if err != nil || device != deviceCPU {
				t.Fatalf("admission: %s %v", device, err)
			}
			if admitted.ActiveModel != v3Int8 || admitted.ActiveRevision != v3Int8Rev || admitted.StandInFor != modelParakeetUltraInt8 || admitted.StandInReason != c.reason || !strings.Contains(admitted.StandInDetail, c.detail) {
				t.Fatalf("admitted %s/%s for %q (%s: %s)", admitted.ActiveModel, admitted.ActiveRevision, admitted.StandInFor, admitted.StandInReason, admitted.StandInDetail)
			}
			env, err := resourceLimitsFromEnv().applyToEnv(admitted.ChildEnv(nil), device, admitted.ActiveModel)
			if err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{envSTTModel: v3Int8, "CASSINI_STT_REVISION": v3Int8Rev, envSTTStandInFor: modelParakeetUltraInt8, envSTTStandInReason: c.reason} {
				if got, _ := envValue(env, key); got != want {
					t.Fatalf("child %s=%q, want %q", key, got, want)
				}
			}
			after, _ := rt.modelJobs(rt.ctx)
			if len(after) != len(before) || (c.job != "" && after[0].State != c.job) {
				t.Fatalf("install jobs %+v, were %+v", after, before)
			}
			// The settings and a job's snapshot never carry the stand-in.
			if raw, _ := json.Marshal(admitted); strings.Contains(strings.ToLower(string(raw)), "stand") || strings.Contains(string(raw), admitted.StandInDetail) {
				t.Fatalf("stand-in persisted: %s", raw)
			}
		})
	}
}

// A fresh install has no v3 files: a build waits, or keeps the audio, exactly
// as before. So does the fast tier, whose model replaces nothing, and an
// install whose Ultra is ready runs Ultra.
func TestNoStandInWithoutRetiredFiles(t *testing.T) {
	rt := standInRuntime(t, false, false, false, false)
	_, _, err := rt.admitTranscription(rt.ctx, ultraInt8Settings(), resourceLimitsFromEnv(), false)
	if err == nil || transientResourceError(err) || err.Error() != "speech model "+modelParakeetUltraInt8+" "+ultraInt8Rev+" is not installed" {
		t.Fatalf("fresh install, ordinary job: %v", err)
	}
	job, err := rt.enqueueModel(rt.ctx, modelInfo{ID: modelParakeetUltraInt8, Revision: ultraInt8Rev}, deviceCPU)
	if err != nil {
		t.Fatal(err)
	}
	job.State = "downloading"
	if err := rt.updateModelJob(job); err != nil {
		t.Fatal(err)
	}
	var wait *resourceUnavailableError
	if _, _, err := rt.admitTranscription(rt.ctx, ultraInt8Settings(), resourceLimitsFromEnv(), true); !errors.As(err, &wait) || !wait.awaitsModel {
		t.Fatalf("fresh install, disposal job: %v", err)
	}

	rt = standInRuntime(t, false, false, true, true)
	fast := STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeet110M, ActiveRevision: strings.Repeat("a", 64), Quality: sttQualityFast, DeviceOverride: deviceCPU}
	writeModelInventory(t, rt, false, false)
	if _, admitted, err := rt.admitTranscription(rt.ctx, fast, resourceLimitsFromEnv(), false); err == nil || admitted.StandInFor != "" {
		t.Fatalf("fast tier: %+v %v", admitted, err)
	}

	rt = standInRuntime(t, true, true, true, true)
	if _, admitted, err := rt.admitTranscription(rt.ctx, ultraInt8Settings(), resourceLimitsFromEnv(), false); err != nil || admitted.ActiveModel != modelParakeetUltraInt8 || admitted.StandInFor != "" {
		t.Fatalf("Ultra ready: %+v %v", admitted, err)
	}
	if probes := probesRun(t, rt); len(probes) != 0 {
		t.Fatalf("checked %v", probes)
	}
}

// A delete-after-processing job pinned to v3 before the upgrade stands in
// with its own v3 files, so it is transcribed now instead of waiting; Ultra's
// install is still queued for the recordings after it.
func TestDisposalJobUsesV3StandInAndStillQueuesUltra(t *testing.T) {
	rt := standInRuntime(t, false, false, true, true)
	rt.cfg.WorkRoot = filepath.Join(t.TempDir(), "jobs")
	rt.setSettings(STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeet110M, ActiveRevision: strings.Repeat("a", 64), Quality: sttQualityFast, DeviceOverride: deviceCPU, Source: sttSourceUser})
	limit := rt.buildResourceDeferralLimit()
	job, run := insertRetiredDisposalJob(t, rt, "dispose-v3-stand-in", limit)

	meeting, err := rt.executeBuildCLI(rt.ctx, buildTask{JobID: job.ID, AttemptNumber: 1, ArtifactRunPath: run, DeferralCount: limit})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, err := os.Stat(filepath.Join(meeting, "cassini.json")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(rt.cfg.ModelCacheRoot, "build-env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CASSINI_STT_MODEL=" + v3Int8, "CASSINI_STT_REVISION=" + v3Int8Rev, "CASSINI_STT_STAND_IN_FOR=" + modelParakeetUltraInt8, "CASSINI_STT_STAND_IN_REASON=installing"} {
		if !strings.Contains(string(raw), want+"\n") {
			t.Fatalf("build env lacks %s:\n%s", want, raw)
		}
	}
	if j := onlyModelJob(t, rt); j.Model != modelParakeetUltraInt8 || j.Revision != ultraInt8Rev || j.State != "queued" {
		t.Fatalf("install job %+v", j)
	}
	if _, err := os.Stat(run); err != nil {
		t.Fatalf("source media gone before publication: %v", err)
	}
}

// A stand-in that has not passed its runtime check on this runtime is checked
// before the build loads it. One that fails leaves the build exactly where it
// would have been without a stand-in, and is not checked again.
func TestStandInRunsV3RuntimeCheckOnce(t *testing.T) {
	rt := standInRuntime(t, false, false, true, false)
	device, admitted, err := rt.admitTranscription(rt.ctx, ultraInt8Settings(), resourceLimitsFromEnv(), true)
	if err != nil || device != deviceCPU || admitted.ActiveModel != v3Int8 {
		t.Fatalf("admission: %+v %v", admitted, err)
	}
	if probes := probesRun(t, rt); len(probes) != 1 || probes[0] != v3Int8 {
		t.Fatalf("checks %v", probes)
	}

	rt = standInRuntime(t, false, false, true, false)
	if err := os.WriteFile(filepath.Join(rt.cfg.ModelCacheRoot, "fail-probe-"+v3Int8), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var wait *resourceUnavailableError
	_, admitted, err = rt.admitTranscription(rt.ctx, ultraInt8Settings(), resourceLimitsFromEnv(), true)
	if !errors.As(err, &wait) || !wait.awaitsModel || wait.permanent || admitted.StandInFor != "" || admitted.ActiveModel != modelParakeetUltraInt8 {
		t.Fatalf("failed stand-in check: %+v %v", admitted, err)
	}
	if !strings.Contains(err.Error(), "speech model "+modelParakeetUltraInt8+" unavailable: is not installed; being installed (queued)") {
		t.Fatalf("not the wait it would have been: %v", err)
	}
	if _, _, err := rt.admitTranscription(rt.ctx, ultraInt8Settings(), resourceLimitsFromEnv(), true); !errors.As(err, &wait) {
		t.Fatalf("second admission: %v", err)
	}
	if probes := probesRun(t, rt); len(probes) != 1 {
		t.Fatalf("the failed stand-in was checked again: %v", probes)
	}
	// Readiness no longer sizes builds for it either.
	if model, err := rt.admitModelForDevice(ultraInt8Settings(), deviceCPU); model != "" || err == nil {
		t.Fatalf("readiness: %q %v", model, err)
	}
}

// The CUDA path runs fp32 only: v3 int8 never stands in there, v3 fp32 does,
// and its build still needs the GPU memory every CUDA build needs.
func TestStandInOnCUDAIsFp32Only(t *testing.T) {
	rt := standInRuntime(t, false, false, false, false)
	t.Setenv(envSTTCUDACapable, "1")
	stubNVIDIADevice(t, true)
	origGPU := probeGPUFreeMB
	t.Cleanup(func() { probeGPUFreeMB = origGPU })
	var vram atomic.Int64
	vram.Store(8000)
	probeGPUFreeMB = func() (int, bool) { return int(vram.Load()), true }
	settings := STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetUltraFp32, ActiveRevision: ultraFp32Rev, Quality: sttQualityBalanced, DeviceOverride: deviceCUDA, Source: sttSourceUser}
	if _, err := rt.enqueueModel(rt.ctx, modelInfo{ID: modelParakeetUltraFp32, Revision: ultraFp32Rev}, deviceCUDA); err != nil {
		t.Fatal(err)
	}

	// Only v3 int8 on disk: `models list --device cuda` does not list it, and
	// even a row that did would not stand in.
	models := []modelInfo{
		{ID: modelParakeetUltraFp32, Revision: ultraFp32Rev, Device: deviceCUDA, Kind: "speech", Replaces: []string{modelParakeetV3Fp32}},
		{ID: v3Int8, Revision: v3Int8Rev, Device: deviceCUDA, Kind: "speech", Installed: true, Ready: true, ReplacedBy: modelParakeetUltraFp32},
	}
	b, _ := json.Marshal(models)
	if err := os.WriteFile(filepath.Join(rt.cfg.ModelCacheRoot, "inventory.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, admitted, err := rt.admitTranscription(rt.ctx, settings, resourceLimitsFromEnv(), false); !transientResourceError(err) || admitted.StandInFor != "" {
		t.Fatalf("v3 int8 on CUDA: %+v %v", admitted, err)
	}

	writeStandInInventory(t, rt, deviceCUDA, false, false, true, true)
	device, admitted, err := rt.admitTranscription(rt.ctx, settings, resourceLimitsFromEnv(), false)
	if err != nil || device != deviceCUDA || admitted.ActiveModel != modelParakeetV3Fp32 || admitted.ActiveRevision != v3Fp32Rev || admitted.StandInFor != modelParakeetUltraFp32 {
		t.Fatalf("v3 fp32 on CUDA: %s %+v %v", device, admitted, err)
	}
	if _, err := resourceLimitsFromEnv().applyToEnv(admitted.ChildEnv(nil), device, admitted.ActiveModel); err != nil {
		t.Fatalf("enough VRAM: %v", err)
	}
	vram.Store(7000)
	if _, err := resourceLimitsFromEnv().applyToEnv(admitted.ChildEnv(nil), device, admitted.ActiveModel); !transientResourceError(err) {
		t.Fatalf("a stand-in build under the VRAM floor: %v", err)
	}
}

// A CPU stand-in build is charged its own measured peak.
func TestMinFreeMemForRetiredV3(t *testing.T) {
	limits := resourceLimits{cpuMemHeadroomMB: 1024, minFreeMemMB: 6144}
	for model, want := range map[string]int{v3Int8: 2816, modelParakeetV3Fp32: 4608} {
		if got := limits.minFreeMemForBuild(deviceCPU, model); got != want {
			t.Errorf("%s: %d MiB, want %d", model, got, want)
		}
	}
	if !modelSupportsCUDA(modelParakeetV3Fp32) || modelSupportsCUDA(v3Int8) {
		t.Fatal("only v3 fp32 runs on CUDA")
	}
	if !runtimeSelectsReferenceFrontend(v3Int8, cassini6Runtime) || modelDownloadMB(v3Int8) != 0 {
		t.Fatal("v3 is not the model it was, or has a download")
	}
}

// Readiness sizes the next build for the model it will load.
func TestReadinessSizesBuildsForTheStandIn(t *testing.T) {
	rt := standInRuntime(t, false, false, true, true)
	if model, err := rt.admitModelForDevice(ultraInt8Settings(), deviceCPU); err != nil || model != v3Int8 {
		t.Fatalf("readiness: %q %v", model, err)
	}
}

// The stand-in is set for one build by admission: an inherited value never
// reaches a child.
func TestChildEnvStripsInheritedStandIn(t *testing.T) {
	base := []string{envSTTStandInFor + "=" + modelParakeetUltraInt8, envSTTStandInReason + "=installing", envSTTStandInDetail + "=x", "KEEP=1"}
	env := ultraInt8Settings().ChildEnv(base)
	for _, key := range []string{envSTTStandInFor, envSTTStandInReason, envSTTStandInDetail} {
		if v, ok := envValue(env, key); ok {
			t.Fatalf("inherited %s=%s", key, v)
		}
	}
	if v, _ := envValue(env, "KEEP"); v != "1" {
		t.Fatal("dropped an unrelated key")
	}
}

// A build of the operator holds buildExecutionMu for its whole run. The
// install job's probe, which removes the files of the model it replaces,
// waits for it, so it never removes them under a build standing in.
func TestModelJobProbeWaitsForARunningBuild(t *testing.T) {
	rt := standInRuntime(t, false, false, true, true)
	job, err := rt.enqueueModel(rt.ctx, modelInfo{ID: modelParakeetUltraInt8, Revision: ultraInt8Rev}, deviceCPU)
	if err != nil {
		t.Fatal(err)
	}
	rt.buildExecutionMu.Lock()
	done := make(chan struct{})
	go func() { rt.runModelJob(job); close(done) }()
	awaitModelState(t, rt, "downloading")
	time.Sleep(300 * time.Millisecond)
	if probes := probesRun(t, rt); len(probes) != 0 {
		t.Fatalf("probe ran during a build: %v", probes)
	}
	rt.buildExecutionMu.Unlock()
	<-done
	if probes := probesRun(t, rt); len(probes) != 1 || probes[0] != modelParakeetUltraInt8 {
		t.Fatalf("probes %v", probes)
	}
}
