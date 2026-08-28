package operator

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// errStatusSubstrateProbe stands in for whatever Nextcloud said; the tests care
// about how the failure is reported, not what caused it.
var errStatusSubstrateProbe = errors.New("groupfolders app not enabled")

func TestStatusHandlerReportsCurrentEffectiveCUDASettings(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	t.Setenv("APP_VERSION", "9.9.9")
	// These image/process values are deliberately stale. Build execution is
	// governed by the live settings snapshot and forced-CUDA admission policy,
	// which is what /status must report.
	t.Setenv("CASSINI_STT_DEVICE", "cpu")
	t.Setenv("CASSINI_STT_MODEL", "stale-image-model")
	rt.setSettings(STTSettings{Quality: sttQualityFast})
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
	if resp.STT.ModelID != auditedCUDAParakeetV3 {
		t.Fatalf("model_id = %q, want %s", resp.STT.ModelID, auditedCUDAParakeetV3)
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

	// A later policy update and a second readiness request must use a fresh
	// snapshot and a fresh probe, not the values cached at process start.
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
	if len(probedDevices) != 2 || probedDevices[1] != "cuda" {
		t.Fatalf("probed devices after refresh = %v, want [cuda cuda]", probedDevices)
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

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if resp.OK {
		t.Fatalf("expected ok=false, got %#v", resp)
	}
	if resp.STT.Device != "cuda" || resp.STT.DeviceUsable {
		t.Fatalf("unexpected stt status: %#v", resp.STT)
	}
	if !strings.Contains(resp.STT.Detail, "no NVIDIA device visible") {
		t.Fatalf("expected actionable detail, got %q", resp.STT.Detail)
	}
}

func TestStatusHandlerRejectsLegacyCPUOverride(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.setSettings(STTSettings{Quality: sttQualityBest, DeviceOverride: "cpu"})
	probeCalled := false
	rt.computeProbe = func(device string) (bool, string) {
		probeCalled = true
		return true, "cuda ready"
	}

	rec := httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if resp.STT.Device != "cuda" || resp.STT.DeviceUsable || !strings.Contains(resp.STT.Detail, "GPU-only") {
		t.Fatalf("unexpected legacy override status: %#v", resp.STT)
	}
	if probeCalled {
		t.Fatal("hardware probe ran despite an invalid stored CPU policy")
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
	buf := &syncBuffer{}
	rt := &Runtime{
		logger:       log.New(buf, "", 0),
		settings:     STTSettings{Quality: sttQualityBest},
		computeProbe: func(device string) (bool, string) { return false, "GPU absent" },
	}
	rt.logComputeDeviceStatus()
	out := buf.String()
	if !strings.Contains(out, "ERROR") || !strings.Contains(out, "cuda") || !strings.Contains(out, "GPU absent") {
		t.Fatalf("expected loud unusable-device log, got %q", out)
	}
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

func TestStatusReportsAStandaloneOperatorAsHealthyWithNoSubstrate(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("a standalone operator must not be unhealthy for want of a Nextcloud: %d %#v", rec.Code, resp)
	}
	if resp.RecordingsAccess.Applicable {
		t.Fatalf("recordings access must not be applicable without AppAPI: %#v", resp.RecordingsAccess)
	}
	if !resp.RecordingsAccess.OK || resp.RecordingsAccess.Detail == "" {
		t.Fatalf("an inapplicable substrate must read as OK and say why: %#v", resp.RecordingsAccess)
	}
	if resp.RecordingsAccess.State != string(ncSubstrateNotApplicable) {
		t.Fatalf("state = %q, want %q", resp.RecordingsAccess.State, ncSubstrateNotApplicable)
	}
}

func TestStatusReportsAProvisionedSubstrate(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.succeed()
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("a provisioned substrate must be healthy: %d %#v", rec.Code, resp)
	}
	if !resp.RecordingsAccess.Applicable || !resp.RecordingsAccess.OK {
		t.Fatalf("unexpected recordings access: %#v", resp.RecordingsAccess)
	}
	if resp.RecordingsAccess.CheckedAt == "" {
		t.Fatal("a recorded outcome must carry when it was recorded")
	}
	// The sink is reported so an ExApp pinned to the local sink — where a
	// provisioned substrate is inert rather than wrong — is distinguishable.
	if resp.RecordingsAccess.PublishSink == "" {
		t.Fatal("the resolved publish sink must be reported")
	}
	if resp.RecordingsAccess.State != string(ncSubstrateProvisioned) {
		t.Fatalf("state = %q, want %q", resp.RecordingsAccess.State, ncSubstrateProvisioned)
	}
}

func TestStatusIsUnhealthyWhenTheSubstrateIsMissing(t *testing.T) {
	// The whole point: a Group-folders-less instance answers every request and
	// shows nobody their recordings. That is not healthy, and /status must not
	// call it healthy.
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.unavailable("app_missing:"+ncAppGroupFolders, errStatusSubstrateProbe)
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the substrate is missing; body=%s", rec.Code, rec.Body.String())
	}
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if resp.OK || resp.RecordingsAccess.OK {
		t.Fatalf("expected an unhealthy substrate: %#v", resp.RecordingsAccess)
	}
	// The detail has to be actionable: it names the step and the likely cause.
	if !strings.Contains(resp.RecordingsAccess.Detail, ncAppGroupFolders) {
		t.Fatalf("detail is not actionable: %q", resp.RecordingsAccess.Detail)
	}
	// And the step has to be machine-readable, so a monitor or an install check
	// can key on WHICH failure this is rather than string-matching prose.
	if resp.RecordingsAccess.State != string(ncSubstrateUnavailable) {
		t.Fatalf("state = %q, want %q", resp.RecordingsAccess.State, ncSubstrateUnavailable)
	}
	if resp.RecordingsAccess.Step != "app_missing:"+ncAppGroupFolders {
		t.Fatalf("step = %q, want app_missing:%s", resp.RecordingsAccess.Step, ncAppGroupFolders)
	}
}

// A failed CALL is not the same diagnosis as an ABSENT app: nothing is installed
// to fix it. Both are unhealthy, and the step is what tells them apart.
func TestStatusReportsDegradedAsUnhealthyAndDistinctFromUnavailable(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.degraded("app_check_failed", errStatusSubstrateProbe)
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when provisioning degraded; body=%s", rec.Code, rec.Body.String())
	}
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if resp.RecordingsAccess.State != string(ncSubstrateDegraded) {
		t.Fatalf("state = %q, want %q", resp.RecordingsAccess.State, ncSubstrateDegraded)
	}
	if resp.RecordingsAccess.Step != "app_check_failed" {
		t.Fatalf("step = %q, want app_check_failed", resp.RecordingsAccess.Step)
	}
}

// The administrator and the prerequisite list are context an admin needs to act,
// so they survive into the report rather than living only in the log.
func TestStatusReportsTheResolvedAdministratorAndPrerequisites(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.setAdminUser("ops-root")
	ncAccessSubstrate.setPrerequisites([]ncPrerequisiteStatus{
		{Name: ncAppGroupFolders, State: ncPrerequisiteEnabled},
		{Name: ncAppEveryoneGroup, State: ncPrerequisiteMissing, Detail: "run `occ app:install group_everyone`"},
	})
	ncAccessSubstrate.unavailable("app_missing:"+ncAppEveryoneGroup, nil)
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if resp.RecordingsAccess.AdminUser != "ops-root" {
		t.Fatalf("admin_user = %q, want ops-root", resp.RecordingsAccess.AdminUser)
	}
	if len(resp.RecordingsAccess.Prerequisites) != 2 {
		t.Fatalf("prerequisites = %#v, want both native apps", resp.RecordingsAccess.Prerequisites)
	}
	if resp.RecordingsAccess.Prerequisites[1].State != ncPrerequisiteMissing {
		t.Fatalf("the missing app must be named as missing: %#v", resp.RecordingsAccess.Prerequisites)
	}
}

func TestStatusReportsProvisioningThatHasNotRunYet(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	ncAccessSubstrate.markApplicable()
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if resp.RecordingsAccess.CheckedAt != "" {
		t.Fatalf("nothing has been checked yet: %#v", resp.RecordingsAccess)
	}
	if !strings.Contains(resp.RecordingsAccess.Detail, "has not run yet") {
		t.Fatalf("an ExApp that was never enabled must say so: %q", resp.RecordingsAccess.Detail)
	}
	// Reachable after a bare container restart, because provisioning runs on the
	// enabled edge and a restart is not one (D-541). Reported as `unknown`
	// rather than passed off as a standalone with nothing to provision.
	if resp.RecordingsAccess.State != string(ncSubstrateUnknown) {
		t.Fatalf("state = %q, want %q", resp.RecordingsAccess.State, ncSubstrateUnknown)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 for an unverified substrate", rec.Code)
	}
}

// An ExApp deliberately pinned to CASSINI_PUBLISH_SINK=local serves nothing from
// Nextcloud Files, so a Team folder it never writes to is not its health.
// Provisioning still runs there (idempotent, cheap) and still records — but
// applicability is decided by the deployment, not by whether provisioning
// happened. Without this, `local` — the escape hatch the publish gate points
// at — would be a permanently unhealthy configuration.
func TestStatusDoesNotJudgeALocalSinkOnANextcloudSubstrate(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	// Provisioning ran and failed, but this deployment was never marked
	// applicable because its resolved sink is not nextcloud-files.
	ncAccessSubstrate.unavailable("app_missing:"+ncAppGroupFolders, errStatusSubstrateProbe)
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rt.statusHandler(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a deployment that expects no substrate", rec.Code)
	}
	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if resp.RecordingsAccess.State != string(ncSubstrateNotApplicable) || !resp.RecordingsAccess.OK {
		t.Fatalf("recordings access = %#v, want not_applicable and OK", resp.RecordingsAccess)
	}
}

// The USER-readable half. A non-admin cannot reach /status — that is correct,
// it carries the administrator, the paths and the version — but it left the
// person actually looking at the app with `HTTP 502` as the only account of an
// install that was never finished.

func TestSetupReportsTheSameVerdictAsStatusWithoutTheDiagnosis(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func()
		wantOK  bool
		want    ncSubstrateState
	}{
		{
			name:    "provisioned",
			arrange: func() { ncAccessSubstrate.markApplicable(); ncAccessSubstrate.succeed() },
			wantOK:  true,
			want:    ncSubstrateProvisioned,
		},
		{
			name: "unavailable",
			arrange: func() {
				ncAccessSubstrate.markApplicable()
				ncAccessSubstrate.unavailable("app_missing:"+ncAppGroupFolders, errStatusSubstrateProbe)
			},
			want: ncSubstrateUnavailable,
		},
		{
			name: "degraded",
			arrange: func() {
				ncAccessSubstrate.markApplicable()
				ncAccessSubstrate.degraded("acl_enable", errStatusSubstrateProbe)
			},
			want: ncSubstrateDegraded,
		},
		{
			// A container restarted without the app being re-enabled. Publishing
			// is already refused here, so the viewer must not claim otherwise.
			name:    "unknown",
			arrange: ncAccessSubstrate.markApplicable,
			want:    ncSubstrateUnknown,
		},
		{
			// A standalone operator, or an ExApp pinned to the local sink. Nothing
			// to set up, so nothing to warn anyone about.
			name:    "not applicable",
			arrange: func() {},
			wantOK:  true,
			want:    ncSubstrateNotApplicable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ncAccessSubstrate.reset()
			t.Cleanup(ncAccessSubstrate.reset)
			tc.arrange()
			rt, cleanup := newTestRuntime(t)
			defer cleanup()

			rec := httptest.NewRecorder()
			rt.setupHandler(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))

			// Always 200, even when not OK: the caller is a browser deciding what
			// to render, and it has to be able to tell "Cassini is not set up"
			// from "the ExApp is down" — which is what a 503 in front of it means.
			if rec.Code != http.StatusOK {
				t.Fatalf("setup = %d, want 200 in every state; body=%s", rec.Code, rec.Body.String())
			}
			var resp setupResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode setup response: %v", err)
			}
			if resp.OK != tc.wantOK || resp.State != string(tc.want) {
				t.Fatalf("setup = %#v, want ok=%v state=%s", resp, tc.wantOK, tc.want)
			}
		})
	}
}

// The endpoint is USER-level at the proxy, so what it does NOT say is as much a
// part of its contract as what it does. The step names an app or a config key;
// the detail quotes Nextcloud; admin_user names an account. None of that is a
// non-admin's business, and none of it helps them — their only remedy is to
// tell an administrator.
func TestSetupWithholdsEverythingAdminOnly(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.setAdminUser("ops-root")
	ncAccessSubstrate.setPrerequisites([]ncPrerequisiteStatus{
		{Name: ncAppGroupFolders, State: ncPrerequisiteMissing, Detail: "run `occ app:install groupfolders`"},
	})
	ncAccessSubstrate.unavailable("app_missing:"+ncAppGroupFolders, errStatusSubstrateProbe)
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rt.setupHandler(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))

	body := rec.Body.String()
	for _, leak := range []string{"ops-root", "app_missing", ncAppGroupFolders, "occ", rt.cfg.SiteRoot} {
		if strings.Contains(body, leak) {
			t.Fatalf("setup leaked admin-only detail %q: %s", leak, body)
		}
	}
	var fields map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &fields); err != nil {
		t.Fatalf("decode setup response: %v", err)
	}
	if len(fields) != 2 {
		t.Fatalf("setup must answer with ok+state only, got %#v", fields)
	}
}

func TestSetupIsMountedWhereverTheOperatorAPIIs(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	// Deliberately the BROKEN state: mounted-and-answering must not be confused
	// with healthy. /status answers 503 here, and an implementation that copied
	// that would make this test pass for the wrong reason.
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.unavailable("app_missing:"+ncAppGroupFolders, errStatusSubstrateProbe)
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
