package operator

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func captureDistWith(t *testing.T, files map[string]string) string {
	t.Helper()
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
	for _, name := range []string{captureSWFile, capturePayloadFile, captureWorkerFile} {
		file, ok := captureAssetFileFor(uiAssetURLPrefix + "/" + name)
		if !ok || file != name {
			t.Fatalf("captureAssetFileFor(%q) = %q, %v", name, file, ok)
		}
	}
	for _, path := range []string{
		uiAssetURLPrefix + "/viewer.js",
		uiAssetURLPrefix + "/../../etc/passwd",
		uiAssetURLPrefix + "/nested/capture-sw.js",
		"/capture-sw.js",
	} {
		if _, ok := captureAssetFileFor(path); ok {
			t.Fatalf("captureAssetFileFor(%q) unexpectedly matched", path)
		}
	}
}

// The whole injection chain hangs off this header. A service worker may only
// claim a scope at or below its own script path unless Service-Worker-Allowed
// raises the ceiling, and this script is served from deep inside the AppAPI
// proxy path while the scopes it needs are Talk's call pages. AppAPI's proxy
// forwards response headers verbatim, so what is set here is what the browser
// sees. Nextcloud core does the same for its Files preview worker.
func TestServeCaptureAssetSetsServiceWorkerAllowed(t *testing.T) {
	cfg := ExAppConfig{ViewerDist: captureDistWith(t, map[string]string{
		captureSWFile:      "// sw",
		capturePayloadFile: "// payload",
	})}
	logger := log.New(io.Discard, "", 0)

	rec := httptest.NewRecorder()
	cfg.serveCaptureAsset(rec, httptest.NewRequest(http.MethodGet, uiAssetURLPrefix+"/"+captureSWFile, nil), captureSWFile, logger)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Service-Worker-Allowed"); got != "/" {
		t.Fatalf("Service-Worker-Allowed = %q, want /", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}

	// Only the service worker needs it; the payload and worker are ordinary
	// scripts and should not advertise a scope they never claim.
	rec = httptest.NewRecorder()
	cfg.serveCaptureAsset(rec, httptest.NewRequest(http.MethodGet, uiAssetURLPrefix+"/"+capturePayloadFile, nil), capturePayloadFile, logger)
	if got := rec.Header().Get("Service-Worker-Allowed"); got != "" {
		t.Fatalf("payload carried Service-Worker-Allowed = %q", got)
	}
}

func TestServeCaptureAssetDegradesWithoutDist(t *testing.T) {
	logger := log.New(io.Discard, "", 0)

	rec := httptest.NewRecorder()
	ExAppConfig{}.serveCaptureAsset(rec, httptest.NewRequest(http.MethodGet, "/ui/"+captureSWFile, nil), captureSWFile, logger)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unset dist: status = %d, want 503", rec.Code)
	}

	// Dist configured but the bundle missing (an image built without the
	// capture build step) must not 500.
	cfg := ExAppConfig{ViewerDist: captureDistWith(t, map[string]string{})}
	rec = httptest.NewRecorder()
	cfg.serveCaptureAsset(rec, httptest.NewRequest(http.MethodGet, "/ui/"+captureSWFile, nil), captureSWFile, logger)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing file: status = %d, want 503", rec.Code)
	}
}

func TestServeCaptureAssetRejectsWrites(t *testing.T) {
	cfg := ExAppConfig{ViewerDist: captureDistWith(t, map[string]string{captureSWFile: "// sw"})}
	rec := httptest.NewRecorder()
	cfg.serveCaptureAsset(rec, httptest.NewRequest(http.MethodPost, "/ui/"+captureSWFile, nil), captureSWFile, log.New(io.Discard, "", 0))
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
