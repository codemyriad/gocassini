package operator

import (
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

func captureDistWith(t *testing.T, files map[string]string) string {
	t.Helper()
	t.Setenv(envSourceCaptureEnabled, "1")
	dist := t.TempDir()
	captureDir := filepath.Join(dist, captureSubdir)
	if err := os.MkdirAll(captureDir, 0o755); err != nil {
		t.Fatalf("mkdir capture dist: %v", err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(captureDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dist
}

func TestCaptureAssetFileFor(t *testing.T) {
	for _, name := range []string{capturePayloadFile, captureWorkerFile} {
		file, ok := captureAssetFileFor(uiAssetURLPrefix + "/" + name)
		if !ok || file != name {
			t.Fatalf("captureAssetFileFor(%q) = %q, %v", name, file, ok)
		}
	}
	for _, path := range []string{
		uiAssetURLPrefix + "/viewer.js",
		uiAssetURLPrefix + "/../../etc/passwd",
		uiAssetURLPrefix + "/capture-sw.js",
		uiAssetURLPrefix + "/nested/capture-worker.js",
	} {
		if _, ok := captureAssetFileFor(path); ok {
			t.Fatalf("captureAssetFileFor(%q) unexpectedly matched", path)
		}
	}
}

func TestServeCaptureAssetServesOrdinaryScripts(t *testing.T) {
	cfg := ExAppConfig{ViewerDist: captureDistWith(t, map[string]string{
		capturePayloadFile: "// payload",
	})}
	logger := log.New(io.Discard, "", 0)

	rec := httptest.NewRecorder()
	cfg.serveCaptureAsset(rec, httptest.NewRequest(http.MethodGet, uiAssetURLPrefix+"/"+capturePayloadFile, nil), capturePayloadFile, logger)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Service-Worker-Allowed"); got != "" {
		t.Fatalf("payload carried Service-Worker-Allowed = %q", got)
	}
}

func TestServeCaptureAssetDegradesWithoutDist(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	// The gate is on for this case; what is being tested is the missing-assets
	// path, not the gate.
	t.Setenv(envSourceCaptureEnabled, "1")

	rec := httptest.NewRecorder()
	ExAppConfig{}.serveCaptureAsset(rec, httptest.NewRequest(http.MethodGet, "/ui/"+capturePayloadFile, nil), capturePayloadFile, logger)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unset dist: status = %d, want 503", rec.Code)
	}

	// Dist configured but the bundle missing (an image built without the
	// capture build step) must not 500.
	cfg := ExAppConfig{ViewerDist: captureDistWith(t, map[string]string{})}
	rec = httptest.NewRecorder()
	cfg.serveCaptureAsset(rec, httptest.NewRequest(http.MethodGet, "/ui/"+capturePayloadFile, nil), capturePayloadFile, logger)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing file: status = %d, want 503", rec.Code)
	}
}

func TestServeCaptureAssetRejectsWrites(t *testing.T) {
	cfg := ExAppConfig{ViewerDist: captureDistWith(t, map[string]string{capturePayloadFile: "// payload"})}
	rec := httptest.NewRecorder()
	cfg.serveCaptureAsset(rec, httptest.NewRequest(http.MethodPost, "/ui/"+capturePayloadFile, nil), capturePayloadFile, log.New(io.Discard, "", 0))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestUIAssetHandlerServesCaptureBundles(t *testing.T) {
	cfg := ExAppConfig{ViewerDist: captureDistWith(t, map[string]string{captureWorkerFile: "// worker"})}
	handler := cfg.uiAssetHandler(log.New(io.Discard, "", 0))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, uiAssetURLPrefix+"/"+captureWorkerFile, nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "// worker" {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, uiAssetURLPrefix+"/unknown.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown asset: status = %d, want 404", rec.Code)
	}
}

// The administrator gate is the containment boundary for the whole feature.
// Capture runs by default on this branch, so what has to hold is the opt-out:
// with CASSINI_SOURCE_CAPTURE=0 a browser that still holds the payload from an
// earlier page load achieves nothing, because the ordinary capture scripts
// stop being served and uploads stay forbidden.
func TestCaptureAssetsDisappearWhenAnAdministratorOptsOut(t *testing.T) {
	dist := captureDistWith(t, map[string]string{capturePayloadFile: "// payload"})
	cfg := ExAppConfig{ViewerDist: dist}
	logger := log.New(io.Discard, "", 0)

	// captureDistWith pinned it on; take the opt-out for this case.
	t.Setenv(envSourceCaptureEnabled, "0")

	rec := httptest.NewRecorder()
	cfg.serveCaptureAsset(rec, httptest.NewRequest(http.MethodGet, "/ui/"+capturePayloadFile, nil), capturePayloadFile, logger)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — to a browser this is simply an installation without the feature", rec.Code)
	}
}

// The switch has to reach a client that is already recording: the companion
// cannot retract JavaScript already executing in a call, so the payload asks.
func TestCaptureEnabledHandlerReportsTheAdministratorSwitch(t *testing.T) {
	rt := &Runtime{}

	t.Setenv(envSourceCaptureEnabled, "1")
	rec := httptest.NewRecorder()
	rt.captureEnabledHandler(rec, httptest.NewRequest(http.MethodGet, "/capture/enabled", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"enabled":true,"uploadProtocol":2}` {
		t.Fatalf("body = %s", body)
	}
	// A cached "yes" is exactly the answer that would outlive the switch being
	// turned off.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}

	t.Setenv(envSourceCaptureEnabled, "0")
	rec = httptest.NewRecorder()
	rt.captureEnabledHandler(rec, httptest.NewRequest(http.MethodGet, "/capture/enabled", nil))
	if body := strings.TrimSpace(rec.Body.String()); body != `{"enabled":false,"uploadProtocol":2}` {
		t.Fatalf("body = %s", body)
	}
}

// Both source-audio switches are ON when nothing sets them: this branch exists
// to run the feature, and an integration deploy that forgot to pass them is
// meant to capture and ingest, not to quietly do neither.
//
// The opt-out is any value strconv.ParseBool reads as false, because that is
// what an admin writes without thinking about it. A value it cannot read at
// all is a typo, and a typo lands OFF rather than on the default: a switch
// nobody can read must not be the one that starts collecting microphones.
func TestSourceAudioSwitchesDefaultOnAndFailClosedOnATypo(t *testing.T) {
	switches := []struct {
		env  string
		read func() bool
	}{
		{envSourceCaptureEnabled, sourceCaptureEnabled},
		{envSourceAudioIngestEnabled, sourceAudioIngestEnabled},
	}
	cases := []struct {
		name  string
		set   bool // false: leave the variable unset entirely
		value string
		want  bool
	}{
		{name: "unset", want: true},
		{name: "empty", set: true, value: "", want: true},
		{name: "blank", set: true, value: "   ", want: true},
		{name: "zero", set: true, value: "0", want: false},
		{name: "false", set: true, value: "false", want: false},
		{name: "FALSE", set: true, value: "FALSE", want: false},
		{name: "one", set: true, value: "1", want: true},
		{name: "true", set: true, value: "true", want: true},
		{name: "typo", set: true, value: "ture", want: false},
		{name: "yes is not a bool", set: true, value: "yes", want: false},
		{name: "off is not a bool", set: true, value: "off", want: false},
	}
	for _, sw := range switches {
		for _, tc := range cases {
			t.Run(sw.env+"/"+tc.name, func(t *testing.T) {
				// Unset means unset: the process may well have inherited a
				// value from whoever ran the tests.
				t.Setenv(sw.env, "")
				if !tc.set {
					if err := os.Unsetenv(sw.env); err != nil {
						t.Fatalf("unset %s: %v", sw.env, err)
					}
				} else {
					t.Setenv(sw.env, tc.value)
				}
				if got := sw.read(); got != tc.want {
					t.Fatalf("%s=%q (set=%t) -> %t, want %t", sw.env, tc.value, tc.set, got, tc.want)
				}
			})
		}
	}
}

// A typo is otherwise indistinguishable from a deliberate opt-out, so it is
// reported. Once per distinct value: these switches are read on every
// /capture/enabled poll and every build, and a log that repeats one typo
// forever is a log nobody reads.
func TestParseBoolEnvDefaultReportsAnUnreadableValueOnce(t *testing.T) {
	var logged strings.Builder
	previous := log.Writer()
	flags := log.Flags()
	log.SetOutput(&logged)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(previous); log.SetFlags(flags) })

	// A name nothing else will use, and a different one on every run: the
	// dedupe is package-global and outlives one test, so a fixed name would
	// make this pass once and fail under `go test -count=2`.
	name := fmt.Sprintf("CASSINI_TEST_UNREADABLE_SWITCH_%d", time.Now().UnixNano())
	t.Setenv(name, "ture")
	for i := 0; i < 3; i++ {
		if parseBoolEnvDefault(name, true) {
			t.Fatal("an unreadable value was read as the default instead of off")
		}
	}
	if got := strings.Count(logged.String(), name); got != 1 {
		t.Fatalf("logged %d times, want exactly 1:\n%s", got, logged.String())
	}

	// A different wrong value is a different mistake and is worth saying.
	t.Setenv(name, "flase")
	if parseBoolEnvDefault(name, true) {
		t.Fatal("a second unreadable value was read as the default instead of off")
	}
	if got := strings.Count(logged.String(), name); got != 2 {
		t.Fatalf("logged %d times after a second bad value, want 2:\n%s", got, logged.String())
	}

	// The default still applies once the variable is gone.
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset: %v", err)
	}
	if !parseBoolEnvDefault(name, true) {
		t.Fatal("unset did not fall back to the default")
	}
	if parseBoolEnvDefault(name, false) {
		t.Fatal("unset did not fall back to a false default")
	}
}
