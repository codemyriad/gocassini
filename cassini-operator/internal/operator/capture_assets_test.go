package operator

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
// With it off, a user opting in client-side achieves nothing: the ordinary
// capture scripts disappear and uploads remain forbidden.
func TestCaptureAssetsAreAbsentUntilAnAdministratorEnablesThem(t *testing.T) {
	dist := captureDistWith(t, map[string]string{capturePayloadFile: "// payload"})
	cfg := ExAppConfig{ViewerDist: dist}
	logger := log.New(io.Discard, "", 0)

	// captureDistWith turned it on; turn it back off for this case.
	t.Setenv(envSourceCaptureEnabled, "")

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
	if body := strings.TrimSpace(rec.Body.String()); body != `{"enabled":true}` {
		t.Fatalf("body = %s", body)
	}
	// A cached "yes" is exactly the answer that would outlive the switch being
	// turned off.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}

	t.Setenv(envSourceCaptureEnabled, "")
	rec = httptest.NewRecorder()
	rt.captureEnabledHandler(rec, httptest.NewRequest(http.MethodGet, "/capture/enabled", nil))
	if body := strings.TrimSpace(rec.Body.String()); body != `{"enabled":false}` {
		t.Fatalf("body = %s", body)
	}
}
