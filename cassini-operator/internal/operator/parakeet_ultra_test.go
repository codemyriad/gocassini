package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	cassini6Runtime = "1.13.7+cassini-parakeet-v3-reference-v1.nemotron-diarization-v2"
	cassini7Runtime = cassini6Runtime + ".parakeet-ultra-v1"
)

// Every CUDA tier resolves to Parakeet Ultra; the CPU tiers are unchanged.
func TestModelForQualityUsesUltraOnCUDA(t *testing.T) {
	for _, q := range []string{sttQualityFast, sttQualityBalanced, sttQualityBest} {
		if got := modelForQuality(q, deviceCUDA); got != modelParakeetUltraFp32 {
			t.Errorf("%s on cuda = %q, want %q", q, got, modelParakeetUltraFp32)
		}
	}
	for q, want := range map[string]string{sttQualityFast: modelParakeet110M, sttQualityBalanced: modelParakeetV3Int8, sttQualityBest: modelParakeetV3Fp32} {
		if got := modelForQuality(q, deviceCPU); got != want {
			t.Errorf("%s on cpu = %q, want %q", q, got, want)
		}
	}
}

// Ultra is v3 fp32 post-trained: it is sized, gated and reported like v3 fp32,
// except that its reference frontend needs the Ultra runtime marker.
func TestParakeetUltraSharesV3Fp32Treatment(t *testing.T) {
	if modelBuildPeakMB(modelParakeetUltraFp32) != modelBuildPeakMB(modelParakeetV3Fp32) {
		t.Fatalf("Ultra peak %d, v3 fp32 %d", modelBuildPeakMB(modelParakeetUltraFp32), modelBuildPeakMB(modelParakeetV3Fp32))
	}
	if modelDownloadMB(modelParakeetUltraFp32) <= 0 {
		t.Fatal("Ultra has no download size; the panel would not say how long the wait is")
	}
	if !modelSupportsCUDA(modelParakeetUltraFp32) || !modelSupportsCUDA(modelParakeetV3Fp32) || modelSupportsCUDA(modelParakeetV3Int8) || modelSupportsCUDA(modelParakeet110M) {
		t.Fatal("only the fp32 models run on CUDA")
	}
	if !usesReferenceFrontend(modelParakeetUltraFp32) || usesReferenceFrontend(modelParakeet110M) {
		t.Fatal("reference frontend reporting is wrong")
	}
	cases := []struct {
		model, version string
		want           bool
	}{
		{modelParakeetV3Fp32, cassini6Runtime, true},
		{modelParakeetV3Int8, cassini7Runtime, true},
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

// An install prepared with v3 fp32 on a GPU must survive the default moving to
// Ultra: changing the tier keeps it (every CUDA tier is fp32), and going back
// to it from Ultra is allowed. On the CPU only the tier's own model serves.
func TestModelServesQualityKeepsV3OnCUDA(t *testing.T) {
	for _, q := range []string{sttQualityFast, sttQualityBalanced, sttQualityBest} {
		for _, m := range []string{modelParakeetUltraFp32, modelParakeetV3Fp32} {
			if !modelServesQuality(q, deviceCUDA, m) {
				t.Errorf("%s on cuda refused %s", q, m)
			}
		}
		if modelServesQuality(q, deviceCUDA, modelParakeetV3Int8) {
			t.Errorf("%s on cuda accepted int8", q)
		}
	}
	if modelServesQuality(sttQualityBest, deviceCPU, modelParakeetUltraFp32) {
		t.Error("cpu best accepted Ultra; the CPU tier decision is pending")
	}
	if !modelServesQuality(sttQualityBest, deviceCPU, modelParakeetV3Fp32) {
		t.Error("cpu best refused v3 fp32")
	}
}

func TestPutSettingsKeepsAPreparedV3InstallOnCUDA(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	t.Setenv(envSTTCUDACapable, "1")
	stubNVIDIADevice(t, true)
	rt.cfg.CassiniBin = fakeModelCassini(t, `[{"id":"parakeet-tdt-0.6b-v3","revision":"r1","installed":true,"ready":true,"device":"cuda"}]`)
	rt.cfg.ModelCacheRoot = t.TempDir()
	rt.setSettings(STTSettings{
		TranscriptionEnabled: true, ActiveModel: modelParakeetV3Fp32, ActiveRevision: "r1",
		Quality: sttQualityBalanced, DeviceOverride: deviceCUDA, Source: sttSourceUser,
	})
	rec := httptest.NewRecorder()
	rt.settingsHandler(rec, httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"quality":"best","transcription_enabled":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("changing tier with v3 fp32 prepared on cuda = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if got := rt.currentSettings().ActiveModel; got != modelParakeetV3Fp32 {
		t.Fatalf("active model = %q, want the pinned %q", got, modelParakeetV3Fp32)
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
