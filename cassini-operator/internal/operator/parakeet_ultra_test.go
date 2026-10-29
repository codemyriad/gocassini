package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	cassini6Runtime = "1.13.7+cassini-parakeet-v3-reference-v1.nemotron-diarization-v2"
	cassini7Runtime = cassini6Runtime + ".parakeet-ultra-v1"
)

// Parakeet Ultra replaced v3 on every tier: fp32 on every CUDA tier and the
// CPU best tier, int8 on the CPU balanced tier. The 110M model stays fast.
func TestModelForQualityUsesUltraEverywhere(t *testing.T) {
	for _, q := range []string{sttQualityFast, sttQualityBalanced, sttQualityBest} {
		if got := modelForQuality(q, deviceCUDA); got != modelParakeetUltraFp32 {
			t.Errorf("%s on cuda = %q, want %q", q, got, modelParakeetUltraFp32)
		}
	}
	for q, want := range map[string]string{sttQualityFast: modelParakeet110M, sttQualityBalanced: modelParakeetUltraInt8, sttQualityBest: modelParakeetUltraFp32} {
		if got := modelForQuality(q, deviceCPU); got != want {
			t.Errorf("%s on cpu = %q, want %q", q, got, want)
		}
	}
}

// Both Ultra precisions need the Ultra runtime marker; only fp32 runs on CUDA.
// The int8 build is sized from its measured 1,893 MiB peak.
func TestParakeetUltraTreatment(t *testing.T) {
	if got := modelBuildPeakMB(modelParakeetUltraInt8); got < 1893 || got%512 != 0 {
		t.Fatalf("Ultra int8 peak %d MiB, below the measured 1,893 or off the table's 512 MiB grain", got)
	}
	if modelBuildPeakMB(modelParakeetUltraFp32) != 3584 {
		t.Fatalf("Ultra fp32 peak %d", modelBuildPeakMB(modelParakeetUltraFp32))
	}
	for _, m := range []string{modelParakeetUltraFp32, modelParakeetUltraInt8} {
		if modelDownloadMB(m) <= 0 {
			t.Fatalf("%s has no download size; the panel would not say how long the wait is", m)
		}
		if !usesReferenceFrontend(m) {
			t.Fatalf("%s does not report the reference frontend", m)
		}
	}
	if !modelSupportsCUDA(modelParakeetUltraFp32) || modelSupportsCUDA(modelParakeetUltraInt8) || modelSupportsCUDA(modelParakeet110M) || modelSupportsCUDA(modelParakeetV3Int8) {
		t.Fatal("only fp32 runs on CUDA")
	}
	if usesReferenceFrontend(modelParakeet110M) {
		t.Fatal("the 110M model reports the reference frontend")
	}
	cases := []struct {
		model, version string
		want           bool
	}{
		{modelParakeetUltraInt8, cassini6Runtime, false},
		{modelParakeetUltraInt8, cassini7Runtime, true},
		{modelParakeetUltraFp32, cassini6Runtime, false},
		{modelParakeetUltraFp32, cassini7Runtime, true},
		{modelParakeetUltraFp32, "sherpa=1.13.7", false},
		{modelParakeet110M, cassini7Runtime, false},
	}
	for _, c := range cases {
		if got := runtimeSelectsReferenceFrontend(c.model, c.version); got != c.want {
			t.Errorf("%s on %q: %v, want %v", c.model, c.version, got, c.want)
		}
	}
}

// With v3 gone there is one model per tier and device again: the allowance
// that let any fp32 model serve a CUDA tier existed only for a v3 rollback.
func TestModelServesOnlyItsTier(t *testing.T) {
	for _, q := range []string{sttQualityFast, sttQualityBalanced, sttQualityBest} {
		for _, device := range []string{deviceCPU, deviceCUDA} {
			for _, m := range []string{modelParakeetUltraFp32, modelParakeetUltraInt8, modelParakeet110M, "parakeet-tdt-0.6b-v3"} {
				if got, want := modelServesQuality(q, device, m), m == modelForQuality(q, device); got != want {
					t.Errorf("%s on %s serves %s = %v, want %v", q, device, m, got, want)
				}
			}
		}
	}
}

func TestPutSettingsRefusesAnotherTiersModelOnCUDA(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	t.Setenv(envSTTCUDACapable, "1")
	stubNVIDIADevice(t, true)
	rt.cfg.CassiniBin = fakeModelCassini(t, `[{"id":"parakeet-ultra-0.6b-int8","revision":"r1","installed":true,"ready":true,"device":"cuda"}]`)
	rt.cfg.ModelCacheRoot = t.TempDir()
	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"quality":"best","transcription_enabled":true,"active_model":"parakeet-ultra-0.6b-int8","active_revision":"r1","device_override":"cuda"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("int8 on cuda = %d %s, want 409", rec.Code, rec.Body.String())
	}
}

// retiredInventory is a `cassini models list` answer of this version: Ultra
// replaces v3 at each precision, and neither v3 id is listed.
func retiredInventory(t *testing.T, rt *Runtime, ultraInstalled bool) {
	t.Helper()
	models := []modelInfo{
		{ID: modelParakeet110M, Revision: strings.Repeat("a", 64), Device: deviceCPU, Kind: "speech"},
		{ID: modelParakeetUltraInt8, Revision: strings.Repeat("b", 64), Device: deviceCPU, Kind: "speech", Installed: ultraInstalled, Replaces: []string{"parakeet-tdt-0.6b-v3-int8"}},
		{ID: modelParakeetUltraFp32, Revision: strings.Repeat("c", 64), Device: deviceCPU, Kind: "speech", Installed: ultraInstalled, Replaces: []string{"parakeet-tdt-0.6b-v3"}},
	}
	b, _ := json.Marshal(models)
	if err := os.WriteFile(filepath.Join(rt.cfg.ModelCacheRoot, "inventory.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// An install upgraded from a version that shipped Parakeet v3 moves to the
// Ultra model its tier now uses on its device, persists that, and queues the
// download through the ordinary install job (download, then runtime check).
func TestRetiredV3PinMovesToUltraAndDownloads(t *testing.T) {
	cases := []struct {
		name            string
		pinned, quality string
		cuda            bool
		want            string
		revision        string
	}{
		{"v3 int8 on the CPU", "parakeet-tdt-0.6b-v3-int8", sttQualityBalanced, false, modelParakeetUltraInt8, strings.Repeat("b", 64)},
		{"v3 fp32 on CUDA", "parakeet-tdt-0.6b-v3", sttQualityBalanced, true, modelParakeetUltraFp32, strings.Repeat("c", 64)},
		{"v3 fp32 on the CPU best tier", "parakeet-tdt-0.6b-v3", sttQualityBest, false, modelParakeetUltraFp32, strings.Repeat("c", 64)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt := modelTestRuntime(t)
			if c.cuda {
				t.Setenv(envSTTCUDACapable, "1")
			} else {
				t.Setenv(envSTTCUDACapable, "0")
			}
			stubNVIDIADevice(t, c.cuda)
			retiredInventory(t, rt, false)
			before := STTSettings{TranscriptionEnabled: true, ActiveModel: c.pinned, ActiveRevision: strings.Repeat("9", 64), Quality: c.quality, Source: sttSourceUser, TranscriptionTerms: []string{"Cassini"}}
			if err := Save(rt.settingsPath, before); err != nil {
				t.Fatal(err)
			}
			rt.setSettings(before)

			migrated, err := rt.migrateRetiredSpeechModel(rt.ctx)
			if err != nil || !migrated {
				t.Fatalf("migrated=%v err=%v", migrated, err)
			}
			got := rt.currentSettings()
			if got.ActiveModel != c.want || got.ActiveRevision != c.revision || !got.TranscriptionEnabled || got.Quality != c.quality || len(got.TranscriptionTerms) != 1 {
				t.Fatalf("settings after migration: %+v", got)
			}
			onDisk, err := LoadOrInitSettings(rt.settingsPath)
			if err != nil || onDisk.ActiveModel != c.want || onDisk.ActiveRevision != c.revision {
				t.Fatalf("persisted %+v %v", onDisk, err)
			}
			jobs, err := rt.modelJobs(rt.ctx)
			if err != nil || len(jobs) != 1 || jobs[0].Model != c.want || jobs[0].Revision != c.revision || jobs[0].State != "queued" {
				t.Fatalf("install jobs %+v %v", jobs, err)
			}
			wantDevice := deviceCPU
			if c.cuda {
				wantDevice = deviceCUDA
			}
			if jobs[0].Device != wantDevice {
				t.Fatalf("job device %s, want %s", jobs[0].Device, wantDevice)
			}

			// Once moved, a restart finds nothing to do.
			if again, err := rt.migrateRetiredSpeechModel(rt.ctx); again || err != nil {
				t.Fatalf("second pass migrated=%v err=%v", again, err)
			}
		})
	}
}

// The model worker runs the migration on start and then the download, with the
// job's progress showing in Settings like any install.
func TestModelWorkerInstallsTheReplacementOnStart(t *testing.T) {
	rt := modelTestRuntime(t)
	t.Setenv(envSTTCUDACapable, "0")
	stubNVIDIADevice(t, false)
	retiredInventory(t, rt, false)
	pinned := STTSettings{TranscriptionEnabled: true, ActiveModel: "parakeet-tdt-0.6b-v3-int8", ActiveRevision: "old", Quality: sttQualityBalanced, Source: sttSourceUser}
	if err := Save(rt.settingsPath, pinned); err != nil {
		t.Fatal(err)
	}
	rt.setSettings(pinned)
	rt.startModelWorker()
	// The job's own progress is the event: the fake install reports
	// downloading before anything else, so any later state proves the
	// worker picked the migrated job up.
	deadline := time.Now().Add(60 * time.Second)
	for {
		jobs, err := rt.modelJobs(rt.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) == 1 && jobs[0].State != "queued" {
			if jobs[0].Model != modelParakeetUltraInt8 || jobs[0].Progress.Completed == 0 && jobs[0].Progress.Phase == "" {
				t.Fatalf("job %+v", jobs[0])
			}
			break
		}
		if len(jobs) > 1 || time.Now().After(deadline) {
			t.Fatalf("jobs %+v", jobs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := rt.currentSettings().ActiveModel; got != modelParakeetUltraInt8 {
		t.Fatalf("active model %s", got)
	}
}

// Transcription that is off moves its pin but downloads nothing; an id no
// catalogue ever shipped is not "migrated" to whatever the tier says, and
// readiness names it instead.
func TestRetiredPinWithTranscriptionOffAndUnknownPin(t *testing.T) {
	rt := modelTestRuntime(t)
	t.Setenv(envSTTCUDACapable, "0")
	stubNVIDIADevice(t, false)
	retiredInventory(t, rt, false)

	off := STTSettings{ActiveModel: "parakeet-tdt-0.6b-v3-int8", ActiveRevision: "old", Quality: sttQualityBalanced, Source: sttSourceUser}
	rt.setSettings(off)
	if migrated, err := rt.migrateRetiredSpeechModel(rt.ctx); !migrated || err != nil {
		t.Fatalf("migrated=%v err=%v", migrated, err)
	}
	if got := rt.currentSettings().ActiveModel; got != modelParakeetUltraInt8 {
		t.Fatalf("active model %s", got)
	}
	if jobs, _ := rt.modelJobs(rt.ctx); len(jobs) != 0 {
		t.Fatalf("downloaded with transcription off: %+v", jobs)
	}

	unknown := STTSettings{TranscriptionEnabled: true, ActiveModel: "someone-elses-model", ActiveRevision: "r", Quality: sttQualityBalanced, Source: sttSourceUser}
	rt.setSettings(unknown)
	if migrated, err := rt.migrateRetiredSpeechModel(rt.ctx); migrated || err != nil {
		t.Fatalf("migrated=%v err=%v", migrated, err)
	}
	if got := rt.currentSettings(); got.ActiveModel != "someone-elses-model" {
		t.Fatalf("unknown pin replaced by %s", got.ActiveModel)
	}
	if jobs, _ := rt.modelJobs(rt.ctx); len(jobs) != 0 {
		t.Fatalf("queued a model for an unknown pin: %+v", jobs)
	}
	_, err := rt.admitModelForDevice(unknown, deviceCPU)
	if err == nil || !strings.Contains(err.Error(), "someone-elses-model") {
		t.Fatalf("readiness for an unknown pin: %v", err)
	}
}

// While the replacement downloads, a build waits for it (a transient resource
// condition) instead of publishing audio without a transcript, and readiness
// says the model is being installed rather than missing.
func TestBuildWaitsForAModelBeingInstalled(t *testing.T) {
	rt := modelTestRuntime(t)
	t.Setenv(envSTTCUDACapable, "0")
	stubNVIDIADevice(t, false)
	retiredInventory(t, rt, false)
	settings := STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetUltraInt8, ActiveRevision: strings.Repeat("b", 64), Quality: sttQualityBalanced, DeviceOverride: deviceCPU, Source: sttSourceUser}
	rt.setSettings(settings)

	_, _, err := rt.admitTranscription(rt.ctx, settings, resourceLimitsFromEnv(), false)
	if err == nil || transientResourceError(err) {
		t.Fatalf("missing model with no install running: %v (transient=%v)", err, transientResourceError(err))
	}

	job, err := rt.enqueueModel(rt.ctx, modelInfo{ID: modelParakeetUltraInt8, Revision: strings.Repeat("b", 64)}, deviceCPU)
	if err != nil {
		t.Fatal(err)
	}
	job.State, job.Progress = "downloading", modelProgress{Version: 1, Phase: "downloading", Completed: 1, Total: 4}
	if err := rt.updateModelJob(job); err != nil {
		t.Fatal(err)
	}
	_, _, err = rt.admitTranscription(rt.ctx, settings, resourceLimitsFromEnv(), false)
	if !transientResourceError(err) || !strings.Contains(err.Error(), "downloading, 25%") {
		t.Fatalf("admission while downloading: %v", err)
	}
	if _, err := rt.admitModelForDevice(settings, deviceCPU); err == nil || !strings.Contains(err.Error(), "being installed (downloading, 25%)") {
		t.Fatalf("readiness while downloading: %v", err)
	}
}

// /status names the Ultra-specific problem when the runtime predates the Ultra
// selection, instead of reporting the reference frontend as active because the
// v3 marker is there.
func TestStatusReportsUltraReferenceFrontendFromBuildInfo(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cancel()
	rt.workerWG.Wait()
	rt.cfg.ModelCacheRoot = t.TempDir()
	rt.cfg.CassiniBin = ""
	rt.referenceFrontendProbe = nil
	t.Setenv(envSTTCUDACapable, "0")
	stubNVIDIADevice(t, false)
	rt.setSettings(STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetUltraFp32, Quality: sttQualityBalanced, Source: sttSourceUser})
	for _, c := range []struct {
		version string
		want    bool
	}{{cassini6Runtime, false}, {cassini7Runtime, true}} {
		info := filepath.Join(t.TempDir(), "buildinfo.txt")
		if err := os.WriteFile(info, []byte("sherpa_version="+c.version+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv(envNativeBuildInfo, info)
		rt.refFrontendChecked = rt.refFrontendChecked.AddDate(-1, 0, 0)
		rec := httptest.NewRecorder()
		rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
		var resp statusResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.STT.ReferenceFrontend == nil || *resp.STT.ReferenceFrontend != c.want {
			t.Fatalf("%q: reference_frontend = %v, want %v", c.version, resp.STT.ReferenceFrontend, c.want)
		}
		if !c.want && !strings.Contains(resp.STT.Warning, "Parakeet Ultra") {
			t.Fatalf("%q: warning %q does not name Parakeet Ultra", c.version, resp.STT.Warning)
		}
	}
}

// The CUDA image's runtime comes from deployment/sherpa/build.sh, whose
// build-info file splits the version into key=value lines. When doctor cannot
// answer, /status reads that file, and must find the Ultra marker in it as it
// does in the CPU images' single-line form. The file is rebuilt here from
// build.sh's own printf, so a change to that format is caught here.
func TestStatusReadsUltraReferenceFrontendFromNativeBuildScriptInfo(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "deployment", "sherpa", "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var version, format string
	for _, line := range strings.Split(string(script), "\n") {
		if v, ok := strings.CutPrefix(line, "version="); ok {
			version = v
		}
		if rest, ok := strings.CutPrefix(line, "printf 'sherpa=%s"); ok {
			format = "sherpa=%s" + rest[:strings.Index(rest, "'")]
		}
	}
	if version == "" || format == "" {
		t.Fatal("build.sh no longer has a version= line and a printf 'sherpa=...' build-info line")
	}
	values := []string{version, "cuda", "0c2ee3f3", "b6998671"}
	info := format
	for _, v := range values {
		info = strings.Replace(info, "%s", v, 1)
	}
	if strings.Contains(info, "%s") {
		t.Fatalf("build.sh's build-info printf takes more values than this test supplies: %q", format)
	}
	info = strings.ReplaceAll(info, `\n`, "\n") + "onnxruntime_identity=pinned-upstream-archive\n"

	if got := nativeBuildInfoVersion(info); !strings.Contains(got, "\n"+cassini7Runtime+"\n") {
		t.Fatalf("build.sh's build-info does not read back as %q:\n%s", cassini7Runtime, got)
	}

	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cancel()
	rt.workerWG.Wait()
	rt.cfg.ModelCacheRoot = t.TempDir()
	rt.cfg.CassiniBin = ""
	rt.referenceFrontendProbe = nil
	t.Setenv(envSTTCUDACapable, "0")
	stubNVIDIADevice(t, false)
	path := filepath.Join(t.TempDir(), "cassini-native-buildinfo.txt")
	if err := os.WriteFile(path, []byte(info), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envNativeBuildInfo, path)
	for _, model := range []string{modelParakeetUltraFp32, modelParakeetV3Fp32} {
		rt.setSettings(STTSettings{TranscriptionEnabled: true, ActiveModel: model, Quality: sttQualityBalanced, Source: sttSourceUser})
		rt.refFrontendChecked = rt.refFrontendChecked.AddDate(-1, 0, 0)
		rec := httptest.NewRecorder()
		rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
		var resp statusResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.STT.ReferenceFrontend == nil || !*resp.STT.ReferenceFrontend {
			t.Errorf("%s: reference_frontend = %v, want true from build.sh's build-info (warning %q)", model, resp.STT.ReferenceFrontend, resp.STT.Warning)
		}
	}
}
