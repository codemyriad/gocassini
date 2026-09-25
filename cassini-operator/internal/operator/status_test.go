package operator

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// errStatusSubstrateProbe stands in for whatever Nextcloud said; the tests care
// about how the failure is reported, not what caused it.
var errStatusSubstrateProbe = errors.New("recordings owner unavailable")

func TestStatusHandlerReportsCurrentEffectiveCUDASettings(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	t.Setenv(envTalkSignalingInternalSecret, "")
	defer cleanup()
	t.Setenv("APP_VERSION", "9.9.9")
	// These image/process values are deliberately stale. Build execution is
	// governed by the live settings snapshot and forced-CUDA admission policy,
	// which is what /status must report.
	t.Setenv("CASSINI_STT_DEVICE", "cpu")
	t.Setenv("CASSINI_STT_MODEL", "stale-image-model")
	t.Setenv(envSTTCUDACapable, "1")
	stubNVIDIADevice(t, true)
	rt.setSettings(STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetV3Fp32, Quality: sttQualityFast})
	var probedDevices []string
	rt.computeProbe = func(device string) (bool, string) {
		probedDevices = append(probedDevices, device)
		return true, "cuda ready"
	}
	rt.cfg.TalkSharedSecret = "super-secret-value"

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	rt.statusHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected ok=true, got %#v", resp)
	}
	if resp.Version != "9.9.9" || resp.ImageTag != "9.9.9" {
		t.Fatalf("version/image_tag = %q/%q, want 9.9.9", resp.Version, resp.ImageTag)
	}
	if resp.STT.Device != "cuda" || resp.STT.Quality != sttQualityFast || !resp.STT.DeviceUsable {
		t.Fatalf("unexpected stt status: %#v", resp.STT)
	}
	if resp.STT.ModelID != modelParakeetV3Fp32 {
		t.Fatalf("model_id = %q, want %s", resp.STT.ModelID, modelParakeetV3Fp32)
	}
	if len(probedDevices) != 1 || probedDevices[0] != "cuda" {
		t.Fatalf("probed devices = %v, want [cuda]", probedDevices)
	}
	if !resp.Talk.SecretConfigured || resp.Talk.SignalingInternalSecretConfigured || resp.Talk.BackendURLOverrideConfigured {
		t.Fatalf("unexpected talk status: %#v", resp.Talk)
	}
	if !resp.DB.OK || !resp.Storage.WorkRoot.OK || !resp.Storage.SiteRoot.OK {
		t.Fatalf("unexpected db/storage status: db=%#v storage=%#v", resp.DB, resp.Storage)
	}
	// The endpoint must report secret presence only, never the value.
	if strings.Contains(rec.Body.String(), "super-secret-value") {
		t.Fatal("status response leaked the Talk shared secret")
	}

	// A later policy update uses a fresh settings snapshot. The effective device
	// is unchanged, so its expensive readiness result remains briefly cached.
	rt.setSettings(STTSettings{Quality: sttQualityBest, DeviceOverride: "cuda"})
	second := httptest.NewRecorder()
	rt.statusHandler(second, httptest.NewRequest(http.MethodGet, "/status", nil))
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d body=%s", second.Code, http.StatusOK, second.Body.String())
	}
	var secondResp statusResponse
	if err := json.Unmarshal(second.Body.Bytes(), &secondResp); err != nil {
		t.Fatalf("decode second status response: %v", err)
	}
	if secondResp.STT.Quality != sttQualityBest {
		t.Fatalf("second quality = %q, want best", secondResp.STT.Quality)
	}
	if len(probedDevices) != 1 || probedDevices[0] != "cuda" {
		t.Fatalf("probed devices after cached refresh = %v, want [cuda]", probedDevices)
	}
}

func TestStatusHandlerReportsTalkConfigPresenceOnly(t *testing.T) {
	for _, tc := range []struct {
		name       string
		recording  string
		internal   string
		backendURL string
		want       statusTalk
	}{
		{
			name: "missing",
			// When the internal secret is absent, status surfaces an actionable
			// hint so an admin learns of it before a recording fails (D-447).
			want: statusTalk{
				SignalingInternalSecretHint: signalingInternalSecretHint,
			},
		},
		{
			name:       "configured",
			recording:  "recording-secret-value",
			internal:   "internal-secret-value",
			backendURL: "https://cloud.example.test",
			want: statusTalk{
				SecretConfigured:                  true,
				SignalingInternalSecretConfigured: true,
				BackendURLOverrideConfigured:      true,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, cleanup := newTestRuntime(t)
			defer cleanup()
			rt.computeProbe = func(device string) (bool, string) { return true, "cuda ready" }
			rt.cfg.TalkSharedSecret = tc.recording
			rt.cfg.TalkBackendURL = tc.backendURL
			t.Setenv("CASSINI_TALK_SIGNALING_INTERNAL_SECRET", tc.internal)

			req := httptest.NewRequest(http.MethodGet, "/status", nil)
			rec := httptest.NewRecorder()
			rt.statusHandler(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var resp statusResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode status response: %v", err)
			}
			if resp.Talk != tc.want {
				t.Fatalf("talk status = %#v, want %#v", resp.Talk, tc.want)
			}
			for _, secret := range []string{tc.recording, tc.internal} {
				if secret != "" && strings.Contains(rec.Body.String(), secret) {
					t.Fatalf("status response leaked secret %q", secret)
				}
			}
		})
	}
}

func TestStatusHandlerReportsCudaUnusable(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	t.Setenv(envSTTCUDACapable, "1")
	rt.setSettings(STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetV3Fp32, DeviceOverride: "cuda", Quality: sttQualityBest})
	stubNVIDIADevice(t, true)
	t.Setenv("CASSINI_STT_DEVICE", "cpu") // stale process env must not win
	rt.computeProbe = func(device string) (bool, string) {
		if device != "cuda" {
			t.Fatalf("compute probe device = %q, want cuda", device)
		}
		return false, "no NVIDIA device visible"
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	rt.statusHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected audio-ready ok=true, got %#v", resp)
	}
	if resp.STT.Device != "cuda" || resp.STT.DeviceUsable {
		t.Fatalf("unexpected stt status: %#v", resp.STT)
	}
	if !strings.Contains(resp.STT.Detail, "no NVIDIA device visible") {
		t.Fatalf("expected actionable detail, got %q", resp.STT.Detail)
	}
	if !resp.DB.OK || !resp.Storage.WorkRoot.OK || !resp.Storage.SiteRoot.OK || !resp.RecordingsAccess.OK {
		t.Fatalf("CUDA must be the sole failed readiness dimension: %#v", resp)
	}
}

func TestStatusHandlerRejectsPortableImageOnGPUHost(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	t.Setenv(envSTTCUDACapable, "0")
	rt.setSettings(STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetV3Fp32, Quality: sttQualityBest, DeviceOverride: "cuda"})
	probeCalled := false
	rt.computeProbe = func(device string) (bool, string) {
		probeCalled = true
		return true, "cuda hardware visible"
	}

	rec := httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if resp.STT.DeviceUsable || !strings.Contains(resp.STT.Detail, "portable image") || !strings.Contains(resp.STT.Detail, "-cuda") {
		t.Fatalf("unexpected portable-image status: %#v", resp.STT)
	}
	if probeCalled {
		t.Fatal("GPU hardware probe ran even though the image has no CUDA runtime")
	}
}

func TestImageCUDACapabilityFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        bool
	}{
		{name: "declared CUDA", value: "1", want: true},
		{name: "portable", value: "0", want: false},
		{name: "missing", value: "", want: false},
		{name: "invalid", value: "maybe", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envSTTCUDACapable, tc.value)
			got, detail := imageCUDACapability()
			if got != tc.want {
				t.Fatalf("imageCUDACapability() = %t, want %t (%s)", got, tc.want, detail)
			}
			if !got && strings.TrimSpace(detail) == "" {
				t.Fatal("unusable image capability has no actionable detail")
			}
		})
	}
}

func TestStatusHandlerReportsPinnedCPUAsReady(t *testing.T) {
	// A pinned CPU device is a supported policy on any image: readiness must
	// describe it, not fail the install because the image has no CUDA (D-702).
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	t.Setenv(envSTTCUDACapable, "0")
	rt.setSettings(STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetV3Fp32, Quality: sttQualityBest, DeviceOverride: deviceCPU})
	var probedDevices []string
	rt.computeProbe = func(device string) (bool, string) {
		probedDevices = append(probedDevices, device)
		return probeComputeDevice(device)
	}

	rec := httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if resp.STT.Device != deviceCPU || !resp.STT.DeviceUsable {
		t.Fatalf("pinned CPU reported unusable: %#v", resp.STT)
	}
	if resp.STT.ModelID != modelParakeetV3Fp32 {
		t.Fatalf("model_id = %q, want %s (best on CPU is fp32)", resp.STT.ModelID, modelParakeetV3Fp32)
	}
	if len(probedDevices) != 1 || probedDevices[0] != deviceCPU {
		t.Fatalf("probed devices = %v, want [cpu]", probedDevices)
	}
}

func TestStatusHandlerRejectsUnknownDeviceOverride(t *testing.T) {
	// A device the operator cannot execute is still unhealthy, and must be
	// rejected before any hardware probe runs.
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.setSettings(STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetV3Fp32, Quality: sttQualityBest, DeviceOverride: "tpu"})
	probeCalled := false
	rt.computeProbe = func(string) (bool, string) {
		probeCalled = true
		return true, "ready"
	}

	rec := httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if resp.STT.DeviceUsable || !strings.Contains(resp.STT.Detail, "not a device this operator can run on") {
		t.Fatalf("unexpected unknown-override status: %#v", resp.STT)
	}
	if probeCalled {
		t.Fatal("hardware probe ran despite an unexecutable stored device policy")
	}
}

func TestStatusHandlerMountedUnderBasePath(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.computeProbe = func(device string) (bool, string) { return true, "cuda ready" }
	rt.cfg.BasePath = "/operator"
	handler := newHTTPHandler(log.New(ioDiscard{}, "", 0), rt, ExAppConfig{})

	prefixed := httptest.NewRequest(http.MethodGet, "/operator/status", nil)
	prefixedRec := httptest.NewRecorder()
	handler.ServeHTTP(prefixedRec, prefixed)
	if prefixedRec.Code != http.StatusOK {
		t.Fatalf("prefixed status = %d, want %d body=%s", prefixedRec.Code, http.StatusOK, prefixedRec.Body.String())
	}

	root := httptest.NewRequest(http.MethodGet, "/status", nil)
	rootRec := httptest.NewRecorder()
	handler.ServeHTTP(rootRec, root)
	if rootRec.Code != http.StatusNotFound {
		t.Fatalf("root status = %d, want %d", rootRec.Code, http.StatusNotFound)
	}

	rt.cfg.BasePath = "/"
	rootHandler := newHTTPHandler(log.New(ioDiscard{}, "", 0), rt, ExAppConfig{})
	rootMounted := httptest.NewRequest(http.MethodGet, "/status", nil)
	rootMountedRec := httptest.NewRecorder()
	rootHandler.ServeHTTP(rootMountedRec, rootMounted)
	if rootMountedRec.Code != http.StatusOK {
		t.Fatalf("root-mounted status = %d, want %d body=%s", rootMountedRec.Code, http.StatusOK, rootMountedRec.Body.String())
	}
}

func TestProbeComputeDeviceCPUVariants(t *testing.T) {
	for _, device := range []string{"", "cpu", "CPU", "auto"} {
		usable, detail := probeComputeDevice(device)
		if !usable {
			t.Fatalf("probeComputeDevice(%q) = unusable (%s), want usable", device, detail)
		}
	}
	usable, detail := probeComputeDevice("tpu")
	if usable || !strings.Contains(detail, "unknown CASSINI_STT_DEVICE") {
		t.Fatalf("probeComputeDevice(tpu) = %t %q, want unusable with actionable detail", usable, detail)
	}
}

func TestLogComputeDeviceStatusLoudWhenUnusable(t *testing.T) {
	t.Setenv(envSTTCUDACapable, "1")
	buf := &syncBuffer{}
	stubNVIDIADevice(t, true)
	rt := &Runtime{
		logger:       log.New(buf, "", 0),
		settings:     STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetV3Fp32, Quality: sttQualityBest},
		computeProbe: func(device string) (bool, string) { return false, "GPU absent" },
	}
	rt.logComputeDeviceStatus()
	out := buf.String()
	if !strings.Contains(out, "ERROR") || !strings.Contains(out, "cuda") || !strings.Contains(out, "GPU absent") {
		t.Fatalf("expected loud unusable-device log, got %q", out)
	}
}

func TestLogComputeDeviceStatusWarnsWhenACUDAImageFallsBackToCPU(t *testing.T) {
	// Falling back is correct, but a -cuda image deployed on a GPU daemon that
	// suddenly has no GPU is a host problem worth shouting about: the build
	// still runs, an order of magnitude slower.
	t.Setenv(envSTTCUDACapable, "1")
	stubNVIDIADevice(t, false)
	buf := &syncBuffer{}
	rt := &Runtime{
		logger:       log.New(buf, "", 0),
		settings:     STTSettings{Quality: sttQualityBalanced},
		computeProbe: func(device string) (bool, string) { return probeComputeDevice(device) },
	}
	rt.logComputeDeviceStatus()
	out := buf.String()
	if !strings.Contains(out, "stt_device -> cpu") {
		t.Fatalf("expected the resolved CPU device to be logged, got %q", out)
	}
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "no NVIDIA device is visible") {
		t.Fatalf("expected a warning that the CUDA image lost its GPU, got %q", out)
	}
}

// stubNVIDIADevice fixes NVIDIA device visibility for one test, so the device
// decision can be exercised on hosts with and without a GPU.
func stubNVIDIADevice(t *testing.T, present bool) {
	t.Helper()
	orig := probeNVIDIADevice
	probeNVIDIADevice = func() bool { return present }
	t.Cleanup(func() { probeNVIDIADevice = orig })
}

func TestTTLProbeSingleflightAndTTL(t *testing.T) {
	var runs atomic.Int32
	gate := make(chan struct{})
	probe := newTTLProbe(80*time.Millisecond, func() error {
		runs.Add(1)
		<-gate
		return nil
	})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = probe.check()
		}()
	}
	time.Sleep(20 * time.Millisecond) // let callers pile up behind the inflight run
	close(gate)
	wg.Wait()
	if got := runs.Load(); got != 1 {
		t.Fatalf("concurrent checks ran the probe %d times, want 1", got)
	}

	time.Sleep(100 * time.Millisecond) // expire the TTL
	if err := probe.check(); err != nil {
		t.Fatalf("check after TTL error = %v", err)
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("post-TTL check ran the probe %d times total, want 2", got)
	}
}

func TestComputeStatusProbeSingleflightTTLAndDeviceKey(t *testing.T) {
	var runs atomic.Int32
	gate := make(chan struct{})
	probe := newComputeStatusProbe(80*time.Millisecond, func(device string) (bool, string) {
		runs.Add(1)
		if device == "cuda" {
			<-gate
		}
		return true, device + " ready"
	})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			usable, detail := probe.check("cuda")
			if !usable || detail != "cuda ready" {
				t.Errorf("check(cuda) = %t %q", usable, detail)
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()
	if got := runs.Load(); got != 1 {
		t.Fatalf("concurrent CUDA checks ran %d probes, want 1", got)
	}

	if usable, detail := probe.check("other"); !usable || detail != "other ready" {
		t.Fatalf("device-key invalidation = %t %q", usable, detail)
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("new device did not invalidate cache: runs=%d", got)
	}

	time.Sleep(100 * time.Millisecond)
	if usable, detail := probe.check("other"); !usable || detail != "other ready" {
		t.Fatalf("post-TTL check = %t %q", usable, detail)
	}
	if got := runs.Load(); got != 3 {
		t.Fatalf("post-TTL runs=%d, want 3", got)
	}
}

func TestHealthzRecordCheckSingleflightAndCached(t *testing.T) {
	rt, cleanup, logPath, _ := newCLITestRuntime(t)
	defer cleanup()

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/healthz?check=record", nil)
		rec := httptest.NewRecorder()
		rt.healthzHandler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
	}

	logText := readFileString(t, logPath)
	if got := strings.Count(logText, "doctor --target record"); got != 1 {
		t.Fatalf("doctor invocations = %d, want 1 (TTL cache + singleflight), log:\n%s", got, logText)
	}
}

func TestHealthzRecordCheckBoundsWedgedDoctor(t *testing.T) {
	rt, cleanup, _, _ := newCLITestRuntime(t)
	defer cleanup()
	t.Setenv("FAKE_CASSINI_DOCTOR_HANG", "1")
	rt.recordHealthTimeout = 300 * time.Millisecond

	start := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/healthz?check=record", nil)
	rec := httptest.NewRecorder()
	rt.healthzHandler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("wedged doctor was not bounded: took %s", elapsed)
	}
	if !strings.Contains(rec.Body.String(), "deadline") {
		t.Fatalf("expected deadline error in body, got %s", rec.Body.String())
	}
}

func encodeAppAPIAuth(userID, secret string) string {
	return base64.StdEncoding.EncodeToString([]byte(userID + ":" + secret))
}

func TestBearerTokenGuardsStandaloneJobAPI(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.APIToken = "sekrit-token"
	handler := newHTTPHandler(log.New(ioDiscard{}, "", 0), rt, ExAppConfig{})

	noAuth := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	noAuthRec := httptest.NewRecorder()
	handler.ServeHTTP(noAuthRec, noAuth)
	if noAuthRec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth status = %d, want %d", noAuthRec.Code, http.StatusUnauthorized)
	}

	wrong := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	wrong.Header.Set("Authorization", "Bearer nope")
	wrongRec := httptest.NewRecorder()
	handler.ServeHTTP(wrongRec, wrong)
	if wrongRec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-token status = %d, want %d", wrongRec.Code, http.StatusUnauthorized)
	}

	right := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	right.Header.Set("Authorization", "Bearer sekrit-token")
	rightRec := httptest.NewRecorder()
	handler.ServeHTTP(rightRec, right)
	if rightRec.Code != http.StatusOK {
		t.Fatalf("right-token status = %d, want %d body=%s", rightRec.Code, http.StatusOK, rightRec.Body.String())
	}

	// Unauthenticated infrastructure endpoints are unaffected.
	health := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthRec := httptest.NewRecorder()
	handler.ServeHTTP(healthRec, health)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want %d", healthRec.Code, http.StatusOK)
	}
}

func TestBearerTokenOffByDefault(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	handler := newHTTPHandler(log.New(ioDiscard{}, "", 0), rt, ExAppConfig{})

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (bearer auth must be off by default)", rec.Code, http.StatusOK)
	}
}

func TestBearerTokenSkipsAppAPIAuthenticatedRequests(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.APIToken = "sekrit-token"
	exapp := ExAppConfig{Active: true, AppID: "gocassini", AppVersion: "0.1.0", AppSecret: "app-secret"}
	handler := newHTTPHandler(log.New(ioDiscard{}, "", 0), rt, exapp)

	// A request the AppAPI middleware authenticated needs no bearer token:
	// the Nextcloud proxy path keeps working with the token set.
	proxied := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	proxied.Header.Set("AUTHORIZATION-APP-API", encodeAppAPIAuth("admin", "app-secret"))
	proxied.Header.Set("EX-APP-ID", "gocassini")
	proxied.Header.Set("EX-APP-VERSION", "0.1.0")
	proxiedRec := httptest.NewRecorder()
	handler.ServeHTTP(proxiedRec, proxied)
	if proxiedRec.Code != http.StatusOK {
		t.Fatalf("proxied status = %d, want %d body=%s", proxiedRec.Code, http.StatusOK, proxiedRec.Body.String())
	}

	// Without AppAPI headers the middleware itself rejects the request.
	bare := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	bareRec := httptest.NewRecorder()
	handler.ServeHTTP(bareRec, bare)
	if bareRec.Code != http.StatusUnauthorized {
		t.Fatalf("bare status = %d, want %d", bareRec.Code, http.StatusUnauthorized)
	}
}

func TestJobMutationLogsIncludeAppAPIUser(t *testing.T) {
	logBuf := &syncBuffer{}
	logger := log.New(logBuf, "", 0)
	rt, cleanup := newTestRuntimeWithLogger(t, logger)
	defer cleanup()
	exapp := ExAppConfig{Active: true, AppID: "gocassini", AppVersion: "0.1.0", AppSecret: "app-secret"}
	handler := newHTTPHandler(logger, rt, exapp)

	withAuth := func(req *http.Request) *http.Request {
		req.Header.Set("AUTHORIZATION-APP-API", encodeAppAPIAuth("alice", "app-secret"))
		req.Header.Set("EX-APP-ID", "gocassini")
		req.Header.Set("EX-APP-VERSION", "0.1.0")
		return req
	}

	create := withAuth(httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/call"}`)))
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d body=%s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var resp createJobResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	waitForJobState(t, rt.store, resp.ID, "succeeded")

	rerun := withAuth(httptest.NewRequest(http.MethodPost, "/jobs/"+resp.ID+"/rerun", nil))
	rerunRec := httptest.NewRecorder()
	handler.ServeHTTP(rerunRec, rerun)
	if rerunRec.Code != http.StatusAccepted {
		t.Fatalf("rerun status = %d, want %d body=%s", rerunRec.Code, http.StatusAccepted, rerunRec.Body.String())
	}
	waitForJobState(t, rt.store, resp.ID, "succeeded")

	logText := logBuf.String()
	assertLogLineWithUser := func(prefix string) {
		t.Helper()
		for _, line := range strings.Split(logText, "\n") {
			if strings.Contains(line, prefix) && strings.Contains(line, "user=alice") {
				return
			}
		}
		t.Fatalf("no %q log line carrying user=alice, log:\n%s", prefix, logText)
	}
	assertLogLineWithUser("accepted id=" + resp.ID)
	assertLogLineWithUser("rerun accepted id=" + resp.ID)
}

// The substrate block exists because provisioning used to fail into a log line
// and nothing else: the operator stayed "healthy" while serving nobody their
// recordings (D-554 outcome 3, D-545 AC-7).

func TestSetupReportsWhichAICapabilitiesAreConfigured(t *testing.T) {
	provider := LLMProvider{ID: "p-1", Name: "local", BaseURL: "http://127.0.0.1:11434/v1"}
	for _, tc := range []struct {
		name          string
		llm           LLMSettings
		wantSummaries bool
		wantInsights  bool
	}{
		{
			// The out-of-the-box deployment: the transcript is complete and
			// nothing is summarised, which the app has to be able to say.
			name: "no endpoint at all",
		},
		{
			// Registering a provider IS the setup an insight needs: it is the
			// one thing an administrator says that means "this deployment may
			// talk to that endpoint", and insightEndpoint hands it to the
			// child, so the question somebody types has somewhere to go.
			// Summarising is the separate opt-in and stays off.
			name:         "an endpoint, but no step points at it",
			llm:          LLMSettings{Providers: []LLMProvider{provider}},
			wantInsights: true,
		},
		{
			// Insight creation needs less than summarising does — an endpoint
			// it can reach, and nothing else — so it is available here while
			// summaries are not.
			name:         "an endpoint the insight step alone uses",
			llm:          LLMSettings{Providers: []LLMProvider{provider}, Insight: LLMStep{Enabled: true, Provider: provider.ID}},
			wantInsights: true,
		},
		{
			// The insight step has no endpoint of its own and inherits the
			// summary one, which is a configuration an insight will reach.
			name:          "an endpoint, summarising on",
			llm:           LLMSettings{Providers: []LLMProvider{provider}, Summary: LLMStep{Enabled: true, Provider: provider.ID}},
			wantSummaries: true,
			wantInsights:  true,
		},
		{
			// Disabling publish-time summaries leaves the selected provider in
			// place for an insight somebody explicitly requests.
			name:         "an endpoint selected, summarising off",
			llm:          LLMSettings{Providers: []LLMProvider{provider}, Summary: LLMStep{Enabled: false, Provider: provider.ID}},
			wantInsights: true,
		},
		{
			// A step enabled against an endpoint that has since been deleted
			// will not run. Reporting it as on would have the app promise a
			// summary that never arrives. An insight is unaffected: a provider
			// row still exists, and that is all one needs.
			name:         "summarising on, its endpoint gone",
			llm:          LLMSettings{Providers: []LLMProvider{provider}, Summary: LLMStep{Enabled: true, Provider: "p-deleted"}},
			wantInsights: true,
		},
		{
			// No provider at all is the one state in which an insight has
			// nothing to ask, and the only one the locked card belongs in.
			name: "no endpoint at all",
			llm:  LLMSettings{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, cleanup := newTestRuntime(t)
			defer cleanup()
			rt.setLLMSettings(tc.llm)

			rec := httptest.NewRecorder()
			rt.setupHandler(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))

			var resp setupResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode setup response: %v", err)
			}
			if resp.Features.Summaries != tc.wantSummaries || resp.Features.Insights != tc.wantInsights {
				t.Fatalf("setup features = %#v, want summaries=%v insights=%v",
					resp.Features, tc.wantSummaries, tc.wantInsights)
			}
		})
	}
}

// The readiness signal must survive a deployment that cannot serve recordings
// at all: the two facts are independent, and an unset-up install is exactly
// where somebody is trying to work out what is missing.
func TestSetupReportsFeaturesEvenWhenNotSetUp(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.unavailable(storageStepServiceAccount, errStatusSubstrateProbe)
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.setLLMSettings(LLMSettings{
		Providers: []LLMProvider{{ID: "p-1", Name: "local", BaseURL: "http://127.0.0.1:11434/v1"}},
		Summary:   LLMStep{Enabled: true, Provider: "p-1"},
	})

	rec := httptest.NewRecorder()
	rt.setupHandler(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))

	var resp setupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode setup response: %v", err)
	}
	if resp.OK || !resp.Features.Insights {
		t.Fatalf("setup = %#v, want ok=false with insights still reported", resp)
	}
}

func TestSetupIsMountedWhereverTheOperatorAPIIs(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	// Deliberately the BROKEN state: mounted-and-answering must not be confused
	// with healthy. /status answers 503 here, and an implementation that copied
	// that would make this test pass for the wrong reason.
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.unavailable(storageStepServiceAccount, errStatusSubstrateProbe)
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	for _, tc := range []struct {
		basePath string
		url      string
		want     int
	}{
		// The ExApp shape: the manifest route is operator/setup.
		{basePath: "/operator", url: "/operator/setup", want: http.StatusOK},
		{basePath: "/operator", url: "/setup", want: http.StatusNotFound},
		// The standalone shape. Forgetting this branch is how a route lands that
		// works through Nextcloud and 404s in dev.
		{basePath: "/", url: "/setup", want: http.StatusOK},
	} {
		rt.cfg.BasePath = tc.basePath
		handler := newHTTPHandler(log.New(ioDiscard{}, "", 0), rt, ExAppConfig{})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
		if rec.Code != tc.want {
			t.Fatalf("GET %s with base %q = %d, want %d", tc.url, tc.basePath, rec.Code, tc.want)
		}
	}
}

func TestSetupRejectsNonGET(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rt.setupHandler(rec, httptest.NewRequest(http.MethodPost, "/setup", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /setup = %d, want 405", rec.Code)
	}
}

func TestStatusHandlerMissingOptionalModelDoesNotBlockAudio(t *testing.T) {
	// Model installation has its own status; audio remains healthy.
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.ModelCacheRoot = t.TempDir()
	t.Setenv(envSTTCUDACapable, "0")
	stubNVIDIADevice(t, false)
	rt.setSettings(STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetV3Int8, Quality: sttQualityBalanced, Source: sttSourceUser})
	rt.computeProbe = func(device string) (bool, string) { return probeComputeDevice(device) }

	rec := httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if !resp.STT.DeviceUsable || resp.STT.ModelID != modelParakeetV3Int8 {
		t.Fatalf("unexpected stt status for a downloadable tier: %#v", resp.STT)
	}
}

func TestStatusHandlerReportsReferenceFrontendStatus(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.ModelCacheRoot = t.TempDir()
	t.Setenv(envSTTCUDACapable, "0")
	stubNVIDIADevice(t, false)
	rt.setSettings(STTSettings{TranscriptionEnabled: true, ActiveModel: modelParakeetV3Fp32, Quality: sttQualityBalanced, Source: sttSourceUser})
	rt.computeProbe = func(device string) (bool, string) { return probeComputeDevice(device) }

	// 1. Test with referenceFrontendProbe stubbed to true (patched runtime)
	rt.referenceFrontendProbe = func() (bool, bool) { return true, true }
	rec := httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.STT.ReferenceFrontend == nil || !*resp.STT.ReferenceFrontend {
		t.Fatalf("expected reference_frontend=true, got %#v", resp.STT.ReferenceFrontend)
	}
	if resp.STT.Warning != "" {
		t.Fatalf("expected empty warning, got %q", resp.STT.Warning)
	}
	if !resp.OK {
		t.Fatalf("expected ok=true, got %#v", resp)
	}

	// 2. Test with referenceFrontendProbe stubbed to false (unpatched runtime)
	rt.referenceFrontendProbe = func() (bool, bool) { return true, false }
	rec = httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.STT.ReferenceFrontend == nil || *resp.STT.ReferenceFrontend {
		t.Fatalf("expected reference_frontend=false, got %#v", resp.STT.ReferenceFrontend)
	}
	if !strings.Contains(resp.STT.Warning, "upstream sherpa runtime") {
		t.Fatalf("expected warning about upstream runtime, got %q", resp.STT.Warning)
	}
	if !resp.OK {
		t.Fatalf("unpatched runtime warning must NOT set ok=false: %#v", resp)
	}

	// 3. Test with fallback buildinfo file when CassiniBin is unset
	rt.referenceFrontendProbe = nil
	rt.cfg.CassiniBin = ""
	tmp := t.TempDir()
	infoUnpatched := filepath.Join(tmp, "buildinfo-unpatched.txt")
	if err := os.WriteFile(infoUnpatched, []byte("sherpa=1.13.7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envNativeBuildInfo, infoUnpatched)
	rec = httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.STT.ReferenceFrontend == nil || *resp.STT.ReferenceFrontend {
		t.Fatalf("expected reference_frontend=false via buildinfo fallback, got %#v", resp.STT.ReferenceFrontend)
	}

	// 4. Test with CassiniBin executing a mock cassini binary
	// The fixture emits what `doctor --json` emits. It used to echo the prose
	// the probe grepped — which is the contract D-798 replaced, and the fact
	// that a TEST had to encode a human sentence is the clearest sign it was
	// the wrong one. The summary here is deliberately not the real wording:
	// nothing may depend on it.
	fakeBin := writeFakeCassini(t, `cat <<'JSON'
[{"id":"speech.runtime","status":"ok","summary":"any wording at all"}]
JSON
`)
	rt.referenceFrontendProbe = nil
	rt.cfg.CassiniBin = fakeBin
	rec = httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 5. Test caching: removing the mock binary does not break subsequent status calls
	if err := os.Remove(fakeBin); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.STT.ReferenceFrontend == nil || !*resp.STT.ReferenceFrontend {
		t.Fatalf("expected cached reference_frontend=true after binary removed, got %#v", resp.STT.ReferenceFrontend)
	}
}

func TestReferenceFrontendProbeCoalescesUnknownAndRetries(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.referenceFrontendProbe = nil
	t.Setenv(envNativeBuildInfo, filepath.Join(t.TempDir(), "missing"))
	calls := filepath.Join(t.TempDir(), "calls")
	rt.cfg.CassiniBin = writeFakeCassini(t, "echo call >> '"+calls+"'\n")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if known, _ := rt.probeReferenceFrontend(); known {
				t.Error("unknown runtime reported as known")
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "call\n"); got != 1 {
		t.Fatalf("concurrent unknown probes: got %d subprocesses, want 1", got)
	}
	rt.refFrontendChecked = time.Now().Add(-time.Minute)
	rt.probeReferenceFrontend()
	data, err = os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "call\n"); got != 2 {
		t.Fatalf("expired probe: got %d subprocesses, want 2", got)
	}
}

// D-798: the probe reads a doctor check BY ID, not by grepping its prose.
//
// The stub stands in for `cassini doctor --target build --json`. What matters
// is that rewording a summary cannot change the answer, which is what the
// marker-matching this replaced could not promise.
func TestReferenceFrontendProbeReadsTheCheckByID(t *testing.T) {
	bin := writeFakeDoctor(t, `[{"id":"ffmpeg","status":"ok","summary":"ffmpeg available"},
	  {"id":"speech.runtime","status":"ok","summary":"wording nothing may depend on"}]`)
	rt := &Runtime{}
	known, isReference := rt.doProbeReferenceFrontend(bin)
	if !known || !isReference {
		t.Fatalf("ok on speech.runtime should mean the reference frontend is active: known=%v ref=%v", known, isReference)
	}

	bin = writeFakeDoctor(t, `[{"id":"speech.runtime","status":"warn","summary":"different wording again"}]`)
	known, isReference = rt.doProbeReferenceFrontend(bin)
	if !known || isReference {
		t.Fatalf("warn on speech.runtime should mean it is inactive: known=%v ref=%v", known, isReference)
	}
}

// A doctor that does not report the check leaves the answer unknown rather
// than guessed at — the same discipline the rest of the health work follows.
func TestReferenceFrontendProbeLeavesAnAbsentCheckUnknown(t *testing.T) {
	bin := writeFakeDoctor(t, `[{"id":"ffmpeg","status":"ok","summary":"ffmpeg available"}]`)
	rt := &Runtime{}
	// Falls through to the build-info file, which is absent here.
	t.Setenv(envNativeBuildInfo, filepath.Join(t.TempDir(), "absent"))
	if known, _ := rt.doProbeReferenceFrontend(bin); known {
		t.Error("an absent speech.runtime check was treated as an answer")
	}
}

func writeFakeDoctor(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cassini")
	script := "#!/bin/sh\ncat <<'JSON'\n" + body + "\nJSON\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake doctor: %v", err)
	}
	return path
}
