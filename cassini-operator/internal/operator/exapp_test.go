package operator

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
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

// --- Init progress reporter ---

func TestInitProgressReporterPutsToExAppStatus(t *testing.T) {
	var gotMethod, gotPath, gotBody, gotAuth, gotAppID, gotAppVersion, gotOCS string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody = string(body)
		gotAuth = r.Header.Get("AUTHORIZATION-APP-API")
		gotAppID = r.Header.Get("EX-APP-ID")
		gotAppVersion = r.Header.Get("EX-APP-VERSION")
		gotOCS = r.Header.Get("OCS-APIRequest")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := ExAppConfig{
		NextcloudURL: srv.URL + "/", // trailing slash must be normalized away
		AppID:        "gocassini",
		AppVersion:   "0.1.0",
		AppSecret:    "shh",
	}
	reporter := cfg.initProgressReporter(log.New(&bytes.Buffer{}, "", 0))
	if reporter == nil {
		t.Fatal("expected reporter when NEXTCLOUD_URL, APP_SECRET, and APP_ID are set")
	}
	reporter()

	if gotMethod != http.MethodPut {
		t.Fatalf("method = %q, want PUT", gotMethod)
	}
	// AppAPI 3.0.0 deprecated /ocs/v1.php/apps/app_api/apps/status/{appId};
	// the replacement resolves the app from the EX-APP-ID header instead of
	// a path segment.
	if want := "/ocs/v2.php/apps/app_api/ex-app/status"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if want := `{"progress":100,"error":""}`; gotBody != want {
		t.Fatalf("body = %q, want %q", gotBody, want)
	}
	if wantAuth := base64.StdEncoding.EncodeToString([]byte(":shh")); gotAuth != wantAuth {
		t.Fatalf("AUTHORIZATION-APP-API = %q, want %q", gotAuth, wantAuth)
	}
	if gotAppID != "gocassini" {
		t.Fatalf("EX-APP-ID = %q, want gocassini", gotAppID)
	}
	if gotAppVersion != "0.1.0" {
		t.Fatalf("EX-APP-VERSION = %q, want 0.1.0", gotAppVersion)
	}
	if gotOCS != "true" {
		t.Fatalf("OCS-APIRequest = %q, want true", gotOCS)
	}
}

func TestInitProgressReporterNilWhenUnconfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  ExAppConfig
	}{
		{"no nextcloud url", ExAppConfig{AppID: "gocassini", AppSecret: "shh"}},
		{"no secret", ExAppConfig{NextcloudURL: "http://nc.local", AppID: "gocassini"}},
		{"no app id", ExAppConfig{NextcloudURL: "http://nc.local", AppSecret: "shh"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cfg.initProgressReporter(log.New(&bytes.Buffer{}, "", 0)) != nil {
				t.Fatal("expected nil reporter")
			}
		})
	}
}

// --- ExApp UI registrar (Nextcloud navigation entries) ---

type recordedOCSCall struct {
	Method string
	Path   string
	Body   map[string]any
	Auth   string
	AppID  string
	OCS    string
}

func TestUIRegistrarRegistersTopMenuEntriesAndScripts(t *testing.T) {
	var calls []recordedOCSCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		calls = append(calls, recordedOCSCall{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   body,
			Auth:   r.Header.Get("AUTHORIZATION-APP-API"),
			AppID:  r.Header.Get("EX-APP-ID"),
			OCS:    r.Header.Get("OCS-APIRequest"),
		})
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := ExAppConfig{
		NextcloudURL: srv.URL + "/", // trailing slash must be normalized away
		AppID:        "gocassini",
		AppVersion:   "0.1.0",
		AppSecret:    "shh",
	}
	registrar := cfg.uiRegistrar(log.New(&bytes.Buffer{}, "", 0))
	if registrar == nil {
		t.Fatal("expected registrar when NEXTCLOUD_URL, APP_SECRET, and APP_ID are set")
	}
	registrar()

	if len(calls) != 4 {
		t.Fatalf("got %d OCS calls, want 4 (2 top-menu + 2 script): %+v", len(calls), calls)
	}
	wantAuth := base64.StdEncoding.EncodeToString([]byte(":shh"))
	for i, c := range calls {
		if c.Method != http.MethodPost {
			t.Errorf("call %d: method = %q, want POST", i, c.Method)
		}
		if c.Auth != wantAuth {
			t.Errorf("call %d: AUTHORIZATION-APP-API = %q, want %q", i, c.Auth, wantAuth)
		}
		if c.AppID != "gocassini" {
			t.Errorf("call %d: EX-APP-ID = %q, want gocassini", i, c.AppID)
		}
		if c.OCS != "true" {
			t.Errorf("call %d: OCS-APIRequest = %q, want true", i, c.OCS)
		}
	}

	// Calls 0/2 register the entries, calls 1/3 their bootstrap scripts.
	wantTopMenu := []struct {
		name          string
		adminRequired float64 // JSON numbers decode to float64
	}{{"viewer", 0}, {"control-panel", 1}}
	for i, want := range wantTopMenu {
		c := calls[i*2]
		if c.Path != "/ocs/v2.php/apps/app_api/api/v1/ui/top-menu" {
			t.Fatalf("call %d path = %q, want the ui/top-menu OCS route", i*2, c.Path)
		}
		if c.Body["name"] != want.name {
			t.Errorf("top-menu %d: name = %v, want %q", i, c.Body["name"], want.name)
		}
		if c.Body["adminRequired"] != want.adminRequired {
			t.Errorf("top-menu %q: adminRequired = %v, want %v", want.name, c.Body["adminRequired"], want.adminRequired)
		}
		if c.Body["icon"] != "img/app.svg" {
			t.Errorf("top-menu %q: icon = %v, want img/app.svg", want.name, c.Body["icon"])
		}
		if disp, _ := c.Body["displayName"].(string); disp == "" {
			t.Errorf("top-menu %q: displayName missing", want.name)
		}

		s := calls[i*2+1]
		if s.Path != "/ocs/v2.php/apps/app_api/api/v1/ui/script" {
			t.Fatalf("call %d path = %q, want the ui/script OCS route", i*2+1, s.Path)
		}
		if s.Body["type"] != "top_menu" {
			t.Errorf("script %q: type = %v, want top_menu", want.name, s.Body["type"])
		}
		if s.Body["name"] != want.name {
			t.Errorf("script %d: name = %v, want %q", i, s.Body["name"], want.name)
		}
		// Path is relative to the ExApp root, ".js" appended by Nextcloud.
		if wantPath := "ui/" + want.name; s.Body["path"] != wantPath {
			t.Errorf("script %q: path = %v, want %q", want.name, s.Body["path"], wantPath)
		}
	}
}

func TestUIRegistrarNilWhenUnconfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  ExAppConfig
	}{
		{"no nextcloud url", ExAppConfig{AppID: "gocassini", AppSecret: "shh"}},
		{"no secret", ExAppConfig{NextcloudURL: "http://nc.local", AppID: "gocassini"}},
		{"no app id", ExAppConfig{NextcloudURL: "http://nc.local", AppSecret: "shh"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cfg.uiRegistrar(log.New(&bytes.Buffer{}, "", 0)) != nil {
				t.Fatal("expected nil registrar")
			}
		})
	}
}

func TestUIRegistrarLogsErrorOnOCSFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"ocs":{"meta":{"message":"Top Menu entry could not be registered"}}}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	var logBuf bytes.Buffer
	cfg := ExAppConfig{NextcloudURL: srv.URL, AppID: "gocassini", AppVersion: "0.1.0", AppSecret: "shh"}
	cfg.uiRegistrar(log.New(&logBuf, "", 0))()

	if !strings.Contains(logBuf.String(), "ERROR: exapp ui") {
		t.Fatalf("log output %q should contain an exapp ui ERROR line", logBuf.String())
	}
}

// The bootstrap script paths sent to AppAPI must be served by the operator
// itself: AppAPI rewrites them to proxy fetches of /ui/<entry>.js. This guards
// the registrar JSON and the asset routes against drifting apart.
func TestUIAssetRoutesMatchRegisteredScripts(t *testing.T) {
	root := http.NewServeMux()
	ExAppConfig{}.installRoutes(root, t.TempDir(), log.New(&bytes.Buffer{}, "", 0))

	for _, entry := range topMenuEntries {
		// Same URL AppAPI's ExAppUiMiddleware rewrites the script src to,
		// minus the proxy prefix the operator never sees.
		r := httptest.NewRequest(http.MethodGet, "/ui/"+entry.Name+".js", nil)
		w := httptest.NewRecorder()
		root.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /ui/%s.js: got %d, want 200", entry.Name, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
			t.Fatalf("GET /ui/%s.js: Content-Type %q does not include javascript", entry.Name, ct)
		}
		if !strings.Contains(w.Body.String(), "iframe") {
			t.Fatalf("GET /ui/%s.js: bootstrap body does not build an iframe", entry.Name)
		}

		// And the icon each entry references must resolve too.
		ri := httptest.NewRequest(http.MethodGet, "/"+entry.Icon, nil)
		wi := httptest.NewRecorder()
		root.ServeHTTP(wi, ri)
		if wi.Code != http.StatusOK {
			t.Fatalf("GET /%s: got %d, want 200", entry.Icon, wi.Code)
		}
		if ct := wi.Header().Get("Content-Type"); !strings.Contains(ct, "svg") {
			t.Fatalf("GET /%s: Content-Type %q does not include svg", entry.Icon, ct)
		}
	}
}

func TestUIAssetHandlerUnknownPathAndMethod(t *testing.T) {
	root := http.NewServeMux()
	ExAppConfig{}.installRoutes(root, t.TempDir(), log.New(&bytes.Buffer{}, "", 0))

	r := httptest.NewRequest(http.MethodGet, "/ui/other.js", nil)
	w := httptest.NewRecorder()
	root.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /ui/other.js: got %d, want 404", w.Code)
	}

	rp := httptest.NewRequest(http.MethodPost, "/ui/viewer.js", nil)
	wp := httptest.NewRecorder()
	root.ServeHTTP(wp, rp)
	if wp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /ui/viewer.js: got %d, want 405", wp.Code)
	}

	rh := httptest.NewRequest(http.MethodHead, "/img/app.svg", nil)
	wh := httptest.NewRecorder()
	root.ServeHTTP(wh, rh)
	if wh.Code != http.StatusOK {
		t.Fatalf("HEAD /img/app.svg: got %d, want 200", wh.Code)
	}
	if wh.Body.Len() != 0 {
		t.Fatalf("HEAD /img/app.svg: body should be empty, got %d bytes", wh.Body.Len())
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
