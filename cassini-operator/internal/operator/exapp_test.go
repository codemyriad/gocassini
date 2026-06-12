package operator

import (
	"bytes"
	"encoding/base64"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setExAppEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func clearExAppEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		envAppHost, envAppPort, envAppID, envAppVersion, envAppSecret,
		envAppAPIRequired, envControlPanelDist, envViewerDist,
	} {
		t.Setenv(k, "")
	}
}

func TestLoadExAppConfigInactiveWhenNoSecret(t *testing.T) {
	clearExAppEnv(t)
	cfg, err := LoadExAppConfig()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Active {
		t.Fatal("expected inactive when APP_SECRET unset")
	}
	if cfg.BindAddr != "" {
		t.Fatalf("expected empty BindAddr, got %q", cfg.BindAddr)
	}
}

func TestLoadExAppConfigActiveWithSecret(t *testing.T) {
	clearExAppEnv(t)
	setExAppEnv(t, map[string]string{
		envAppSecret:  "shh",
		envAppID:      "gocassini",
		envAppVersion: "0.1.0",
	})
	cfg, err := LoadExAppConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Active {
		t.Fatal("expected active")
	}
}

func TestLoadExAppConfigRequiredButNoSecretIsError(t *testing.T) {
	clearExAppEnv(t)
	t.Setenv(envAppAPIRequired, "true")
	_, err := LoadExAppConfig()
	if err == nil {
		t.Fatal("expected error when CASSINI_APPAPI_REQUIRED=true and APP_SECRET unset")
	}
	if !strings.Contains(err.Error(), "APP_SECRET") {
		t.Fatalf("error %q should mention APP_SECRET", err)
	}
}

func TestLoadExAppConfigAppHostPortHonored(t *testing.T) {
	clearExAppEnv(t)
	t.Setenv(envAppHost, "127.0.0.1")
	t.Setenv(envAppPort, "9999")
	cfg, err := LoadExAppConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BindAddr != "127.0.0.1:9999" {
		t.Fatalf("BindAddr = %q, want 127.0.0.1:9999", cfg.BindAddr)
	}
}

func TestLoadExAppConfigAppPortAloneUsesDefaultHost(t *testing.T) {
	clearExAppEnv(t)
	t.Setenv(envAppPort, "9999")
	cfg, err := LoadExAppConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BindAddr != "0.0.0.0:9999" {
		t.Fatalf("BindAddr = %q, want 0.0.0.0:9999", cfg.BindAddr)
	}
}

func TestExAppDataPathDefault(t *testing.T) {
	const (
		persist  = "/nc_app_gocassini_data"
		fallback = "/repo/cassini-operator/runtime/jobs.sqlite3"
	)
	cases := []struct {
		name        string
		persistRoot string
		envValue    string
		want        string
	}{
		{"no persist, no env, fallback wins", "", "", fallback},
		{"no persist, env wins", "", "/data/jobs.sqlite3", "/data/jobs.sqlite3"},
		{"persist, env unset, redirected", persist, "", persist + "/operator/jobs.sqlite3"},
		{"persist, env at baked image default, redirected", persist, imageDefaultDBPath, persist + "/operator/jobs.sqlite3"},
		{"persist, explicit env override wins", persist, "/mnt/big-disk/jobs.sqlite3", "/mnt/big-disk/jobs.sqlite3"},
		{"persist with trailing slash, redirected", persist + "/", "", persist + "/operator/jobs.sqlite3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := exAppDataPathDefault(tc.persistRoot, tc.envValue, imageDefaultDBPath, "operator/jobs.sqlite3", fallback)
			if got != tc.want {
				t.Fatalf("exAppDataPathDefault() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestApplyToBindAddrPreservesExistingWhenNoExAppEnv(t *testing.T) {
	// REGRESSION: existing -bind flag / CASSINI_OPERATOR_BIND_ADDR still wins
	// when APP_HOST/APP_PORT are unset.
	cfg := ExAppConfig{}
	if got := cfg.applyToBindAddr("0.0.0.0:4000"); got != "0.0.0.0:4000" {
		t.Fatalf("BindAddr clobbered: got %q", got)
	}
}

func TestApplyToBindAddrOverridesWhenExAppEnvSet(t *testing.T) {
	cfg := ExAppConfig{BindAddr: "0.0.0.0:8080"}
	if got := cfg.applyToBindAddr("0.0.0.0:4000"); got != "0.0.0.0:8080" {
		t.Fatalf("APP_PORT did not win: got %q", got)
	}
}

// --- Static SPA handler tests ---

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSPAHandlerServesIndexForRoot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "<html>index</html>")

	h := spaHandler(dir, "/control-panel", log.New(&bytes.Buffer{}, "", 0))
	r := httptest.NewRequest(http.MethodGet, "/control-panel/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "index") {
		t.Fatalf("body %q does not contain index", w.Body.String())
	}
}

func TestSPAHandlerServesIndexForUnknownSubPath(t *testing.T) {
	// SPA fallback: /control-panel/jobs/abc should serve index.html, NOT 404.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "<html>spa-fallback</html>")

	h := spaHandler(dir, "/control-panel", log.New(&bytes.Buffer{}, "", 0))
	r := httptest.NewRequest(http.MethodGet, "/control-panel/jobs/abc/deep/route", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "spa-fallback") {
		t.Fatalf("got body %q, want SPA fallback to index", w.Body.String())
	}
}

func TestSPAHandlerServesAssetWithContentType(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "<html></html>")
	writeFile(t, filepath.Join(dir, "assets", "app.js"), "console.log('x')")

	h := spaHandler(dir, "/control-panel", log.New(&bytes.Buffer{}, "", 0))
	r := httptest.NewRequest(http.MethodGet, "/control-panel/assets/app.js", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "javascript") {
		t.Fatalf("Content-Type %q does not include javascript", ct)
	}
}

func TestSPAHandlerHEAD(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "<html></html>")
	writeFile(t, filepath.Join(dir, "audio.mp3"), "fake-audio-bytes")

	h := spaHandler(dir, "/viewer", log.New(&bytes.Buffer{}, "", 0))
	r := httptest.NewRequest(http.MethodHead, "/viewer/audio.mp3", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("HEAD response body should be empty, got %d bytes", w.Body.Len())
	}
}

func TestSPAHandlerRejectsPOST(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "<html></html>")

	h := spaHandler(dir, "/control-panel", log.New(&bytes.Buffer{}, "", 0))
	r := httptest.NewRequest(http.MethodPost, "/control-panel/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", w.Code)
	}
}

func TestSPAHandlerNoIndexReturns503(t *testing.T) {
	dir := t.TempDir() // empty
	h := spaHandler(dir, "/control-panel", log.New(&bytes.Buffer{}, "", 0))
	r := httptest.NewRequest(http.MethodGet, "/control-panel/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
}

// --- Integration: lifecycle + static + operator routes under AppAPI middleware ---

func TestNewHTTPHandlerLifecycleBypassesBasePath(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.BasePath = "/operator"

	tmp := t.TempDir()
	exappCfg := ExAppConfig{} // no APP_SECRET -> middleware off

	handler := newHTTPHandlerWithStateDir(log.New(&bytes.Buffer{}, "", 0), rt, exappCfg, tmp)

	r := httptest.NewRequest(http.MethodPut, "/enabled?enabled=1", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT /enabled at root: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestNewHTTPHandlerAppAPIMiddlewareWrapsRoutes(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.BasePath = "/operator"

	tmp := t.TempDir()
	exappCfg := ExAppConfig{
		Active:     true,
		AppID:      "gocassini",
		AppVersion: "0.1.0",
		AppSecret:  "shh",
	}
	handler := newHTTPHandlerWithStateDir(log.New(&bytes.Buffer{}, "", 0), rt, exappCfg, tmp)

	// Without AppAPI headers -> 401
	r := httptest.NewRequest(http.MethodGet, "/operator/jobs", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /operator/jobs: got %d, want 401", w.Code)
	}

	// With valid AppAPI headers -> 200
	r2 := httptest.NewRequest(http.MethodGet, "/operator/jobs", nil)
	r2.Header.Set("AUTHORIZATION-APP-API", base64.StdEncoding.EncodeToString([]byte("alice:shh")))
	r2.Header.Set("EX-APP-ID", "gocassini")
	r2.Header.Set("EX-APP-VERSION", "0.1.0")
	r2.Header.Set("AA-REQUEST-ID", "trace-1")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("authenticated /operator/jobs: got %d, want 200; body=%s", w2.Code, w2.Body.String())
	}
}

func TestNewHTTPHandlerLifecycleAlsoBehindMiddleware(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	tmp := t.TempDir()
	exappCfg := ExAppConfig{
		Active: true, AppID: "gocassini", AppSecret: "shh",
	}
	handler := newHTTPHandlerWithStateDir(log.New(&bytes.Buffer{}, "", 0), rt, exappCfg, tmp)

	r := httptest.NewRequest(http.MethodPut, "/enabled?enabled=1", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("lifecycle without AppAPI header: got %d, want 401", w.Code)
	}

	r2 := httptest.NewRequest(http.MethodPut, "/enabled?enabled=1", nil)
	r2.Header.Set("AUTHORIZATION-APP-API", base64.StdEncoding.EncodeToString([]byte(":shh")))
	r2.Header.Set("EX-APP-ID", "gocassini")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("lifecycle with AppAPI header: got %d, want 200; body=%s", w2.Code, w2.Body.String())
	}
}

func TestNewHTTPHandlerHeartbeatBypassesMiddleware(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.BasePath = "/operator"

	tmp := t.TempDir()
	// Active middleware: heartbeat must still pass without AppAPI headers,
	// because AppAPI's reachability probe does not send them.
	exappCfg := ExAppConfig{
		Active: true, AppID: "gocassini", AppVersion: "0.1.0", AppSecret: "shh",
	}
	handler := newHTTPHandlerWithStateDir(log.New(&bytes.Buffer{}, "", 0), rt, exappCfg, tmp)

	r := httptest.NewRequest(http.MethodGet, "/heartbeat", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /heartbeat no auth: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("GET /heartbeat content-type: got %q, want application/json", ct)
	}
	if !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatalf("GET /heartbeat body: got %q, want status=ok JSON", w.Body.String())
	}

	// HEAD also OK (used by some probes); other verbs rejected.
	rh := httptest.NewRequest(http.MethodHead, "/heartbeat", nil)
	wh := httptest.NewRecorder()
	handler.ServeHTTP(wh, rh)
	if wh.Code != http.StatusOK {
		t.Fatalf("HEAD /heartbeat: got %d, want 200", wh.Code)
	}

	rp := httptest.NewRequest(http.MethodPost, "/heartbeat", nil)
	wp := httptest.NewRecorder()
	handler.ServeHTTP(wp, rp)
	if wp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /heartbeat: got %d, want 405", wp.Code)
	}
}

// newHTTPHandlerWithStateDir is a test-only variant that overrides where the
// lifecycle state file is written. Production code derives this from cfg.DBPath.
func newHTTPHandlerWithStateDir(logger *log.Logger, rt *Runtime, exappCfg ExAppConfig, stateDir string) http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("/jobs", rt.jobsHandler)
	api.HandleFunc("/jobs/", rt.jobDetailHandler)
	api.HandleFunc("/events", rt.eventsHandler)

	root := http.NewServeMux()
	exappCfg.installRoutes(root, stateDir, logger)
	mountBasePathOnto(root, rt.cfg.BasePath, api)

	outer := http.NewServeMux()
	outer.Handle("/heartbeat", heartbeatHandler())
	outer.Handle("/", exappCfg.wrap(root, logger))

	return requestLogger(logger, outer)
}
