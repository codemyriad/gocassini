package operator

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cassini-operator/internal/operator/appapi"
)

// ExApp wiring for the Nextcloud AppAPI build.
//
// This file is the single seam where ExApp-specific behavior layers on top of
// the regular operator runtime:
//
//   - Env vars APP_HOST / APP_PORT replace the bind address (when set).
//   - AppAPI middleware (appapi.Config.Middleware) wraps the root mux when
//     APP_SECRET is set. CASSINI_APPAPI_REQUIRED=true makes the operator
//     refuse to start without a secret.
//   - Lifecycle handlers PUT /enabled and POST /init are mounted at the root
//     (NOT under CASSINI_OPERATOR_BASE_PATH) because AppAPI calls them
//     directly on the container.
//   - Static asset prefixes serve the control-panel admin UI, the viewer
//     SPA, and the published meeting archive from on-disk paths set via
//     CASSINI_CONTROL_PANEL_DIST / CASSINI_VIEWER_DIST / the operator's
//     SiteRoot.
//
// Routing shape inside the ExApp container:
//
//   ROOT mux (wrapped by AppAPI middleware when APP_SECRET set)
//   ├── /enabled                       lifecycle (AppAPI direct call)
//   ├── /init                          lifecycle (AppAPI direct call)
//   ├── /control-panel, /control-panel/*   admin SPA static (ADMIN per manifest)
//   ├── /viewer, /viewer/*                 viewer SPA static  (USER per manifest)
//   ├── /published/*                       site archive       (USER per manifest)
//   └── <BasePath>/jobs, /jobs/, /events   operator JSON API  (ADMIN per manifest)
//
// When APP_SECRET is unset the middleware is a no-op pass-through, which is
// what compose.yml expects for local dev.

const (
	envAppHost            = "APP_HOST"
	envAppPort            = "APP_PORT"
	envAppID              = "APP_ID"
	envAppVersion         = "APP_VERSION"
	envAppSecret          = "APP_SECRET"
	envAppAPIRequired     = "CASSINI_APPAPI_REQUIRED"
	envControlPanelDist   = "CASSINI_CONTROL_PANEL_DIST"
	envViewerDist         = "CASSINI_VIEWER_DIST"
	envNextcloudURL       = "NEXTCLOUD_URL"
	defaultExAppBindHost  = "0.0.0.0"
	defaultExAppBindPort  = "8080"
	controlPanelURLPrefix = "/control-panel"
	viewerURLPrefix       = "/viewer"
	publishedURLPrefix    = "/published"
)

// ExAppConfig holds the AppAPI-derived runtime values resolved from env vars.
type ExAppConfig struct {
	Active           bool
	BindAddr         string
	AppID            string
	AppVersion       string
	AppSecret        string
	NextcloudURL     string // optional; if set, /init reports progress=100 back via OCS
	ControlPanelDist string
	ViewerDist       string
	PublishedDir     string // operator SiteRoot, served read-only at /published
}

// LoadExAppConfig reads ExApp env vars and decides whether the AppAPI build
// is active. Returns an error when CASSINI_APPAPI_REQUIRED=true but APP_SECRET
// is missing — that combination means an ExApp Dockerfile shipped without the
// shared secret AppAPI was supposed to inject.
func LoadExAppConfig() (ExAppConfig, error) {
	cfg := ExAppConfig{
		AppID:            strings.TrimSpace(os.Getenv(envAppID)),
		AppVersion:       strings.TrimSpace(os.Getenv(envAppVersion)),
		AppSecret:        os.Getenv(envAppSecret),
		NextcloudURL:     strings.TrimSpace(os.Getenv(envNextcloudURL)),
		ControlPanelDist: strings.TrimSpace(os.Getenv(envControlPanelDist)),
		ViewerDist:       strings.TrimSpace(os.Getenv(envViewerDist)),
	}
	cfg.Active = strings.TrimSpace(cfg.AppSecret) != ""
	required, err := parseBoolEnv(envAppAPIRequired)
	if err != nil {
		return cfg, err
	}
	if required && !cfg.Active {
		return cfg, fmt.Errorf("%s=true but %s is unset: refusing to start", envAppAPIRequired, envAppSecret)
	}

	host := strings.TrimSpace(os.Getenv(envAppHost))
	port := strings.TrimSpace(os.Getenv(envAppPort))
	if port != "" {
		if _, err := strconv.Atoi(port); err != nil {
			return cfg, fmt.Errorf("invalid %s=%q: %w", envAppPort, port, err)
		}
		if host == "" {
			host = defaultExAppBindHost
		}
		cfg.BindAddr = net.JoinHostPort(host, port)
	} else if host != "" {
		// host without port -> default port
		cfg.BindAddr = net.JoinHostPort(host, defaultExAppBindPort)
	}
	return cfg, nil
}

func parseBoolEnv(name string) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid %s=%q: %w", name, raw, err)
	}
	return b, nil
}

// applyToBindAddr returns the bind address the operator should listen on,
// honoring APP_HOST/APP_PORT when set and otherwise preserving the existing
// flag/env default.
func (c ExAppConfig) applyToBindAddr(existing string) string {
	if c.BindAddr != "" {
		return c.BindAddr
	}
	return existing
}

// installRoutes attaches lifecycle handlers and static prefixes to the given
// root mux. Call BEFORE mounting the operator API mux under BasePath so the
// lifecycle endpoints take precedence at the root.
//
// stateDir is where the lifecycle state JSON file lives (typically the parent
// directory of the operator DB).
func (c ExAppConfig) installRoutes(root *http.ServeMux, stateDir string, logger *log.Logger) {
	lifecycle := &LifecycleHandlers{
		Store:                NewFileLifecycleStore(filepath.Join(stateDir, "app-state.json")),
		Logger:               logger,
		InitProgressReporter: c.initProgressReporter(logger),
	}
	lifecycle.Register(root)

	if c.ControlPanelDist != "" {
		root.Handle(controlPanelURLPrefix, spaHandler(c.ControlPanelDist, controlPanelURLPrefix, logger))
		root.Handle(controlPanelURLPrefix+"/", spaHandler(c.ControlPanelDist, controlPanelURLPrefix, logger))
	}
	if c.ViewerDist != "" {
		root.Handle(viewerURLPrefix, spaHandler(c.ViewerDist, viewerURLPrefix, logger))
		root.Handle(viewerURLPrefix+"/", spaHandler(c.ViewerDist, viewerURLPrefix, logger))
	}
	if c.PublishedDir != "" {
		root.Handle(publishedURLPrefix+"/", publishedHandler(c.PublishedDir, publishedURLPrefix, logger))
	}
}

// initProgressReporter returns a callback that PUTs `progress=100` to AppAPI's
// OCS endpoint so the install command's `--wait-finish` poll can proceed. We
// have no async setup work — Cassini doesn't pre-download models — so the
// reporter fires once and signals completion immediately.
//
// Returns nil when NEXTCLOUD_URL or APP_SECRET is unset; in that mode /init
// answers 200 but no callback is sent. That is fine for local dev and for the
// container-level e2e (which has no Nextcloud to call back to); the
// install-E2E path always injects both.
func (c ExAppConfig) initProgressReporter(logger *log.Logger) func() {
	if c.NextcloudURL == "" || c.AppSecret == "" || c.AppID == "" {
		return nil
	}
	nextcloudURL := strings.TrimRight(c.NextcloudURL, "/")
	// PUT /ocs/v2.php/apps/app_api/ex-app/status replaced the per-app
	// /ocs/v1.php/.../apps/status/{appId} route, deprecated in AppAPI 3.0.0
	// (upstream now calls it setAppInitProgressDeprecated). AppAPI resolves
	// the app from the EX-APP-ID header we already send. v2 also returns real
	// HTTP status codes where v1 wrapped failures in HTTP 200, so the >=300
	// check below actually catches failures.
	statusURL := nextcloudURL + "/ocs/v2.php/apps/app_api/ex-app/status"
	auth := base64.StdEncoding.EncodeToString([]byte(":" + c.AppSecret))
	client := &http.Client{Timeout: 10 * time.Second}
	return func() {
		body := bytes.NewBufferString(`{"progress":100,"error":""}`)
		req, err := http.NewRequest(http.MethodPut, statusURL, body)
		if err != nil {
			logger.Printf("init progress: build request: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("OCS-APIRequest", "true")
		req.Header.Set("AUTHORIZATION-APP-API", auth)
		req.Header.Set("EX-APP-ID", c.AppID)
		req.Header.Set("EX-APP-VERSION", c.AppVersion)
		resp, err := client.Do(req)
		if err != nil {
			// Loud on purpose: until this PUT lands, `app_api:app:register
			// --wait-finish` polls forever and the install appears hung.
			logger.Printf("ERROR: init progress: PUT %s: %v — registration --wait-finish will hang until its timeout", statusURL, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			logger.Printf("ERROR: init progress: PUT %s -> %d: %s — registration --wait-finish will hang until its timeout", statusURL, resp.StatusCode, strings.TrimSpace(string(snippet)))
			return
		}
		logger.Printf("init progress: reported progress=100 to %s", statusURL)
	}
}

// heartbeatHandler answers AppAPI's reachability probe with 200 {"status":"ok"}.
// AppAPI hits GET /heartbeat without any AppAPI auth headers for non-HaRP
// deploys (see app_api's AppAPIService::heartbeatExApp), so this handler MUST
// be mounted outside the AppAPI middleware wrap — otherwise registration fails
// with "heartbeat check failed" after the 10-minute retry window.
func heartbeatHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
}

// wrap wraps the given handler with the AppAPI middleware when this build is
// active. When inactive (no APP_SECRET) the original handler is returned.
func (c ExAppConfig) wrap(next http.Handler, logger *log.Logger) http.Handler {
	if !c.Active {
		return next
	}
	return appapi.Config{
		AppID:      c.AppID,
		AppVersion: c.AppVersion,
		AppSecret:  c.AppSecret,
		Logger:     logger,
	}.Middleware(next)
}

// spaHandler serves a single-page application from `dir`, falling back to
// index.html for any path that doesn't match an on-disk file. The prefix is
// stripped before lookup so /viewer/foo/bar -> dir/foo/bar (or dir/index.html
// when foo/bar doesn't exist).
func spaHandler(dir, urlPrefix string, logger *log.Logger) http.Handler {
	fileServer := http.StripPrefix(urlPrefix, http.FileServer(http.Dir(dir)))
	indexPath := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reject method outright at this prefix; static SPAs only do GET/HEAD.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		relPath := strings.TrimPrefix(r.URL.Path, urlPrefix)
		relPath = strings.TrimPrefix(relPath, "/")
		if relPath == "" || relPath == "index.html" {
			serveSPAIndex(w, r, indexPath, logger)
			return
		}
		full := filepath.Join(dir, relPath)
		// Reject any path that escapes dir (defense-in-depth; FileServer also
		// rejects these but we look at full path here for the SPA fallback).
		if !strings.HasPrefix(full, filepath.Clean(dir)+string(os.PathSeparator)) && full != filepath.Clean(dir) {
			http.NotFound(w, r)
			return
		}
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			serveSPAIndex(w, r, indexPath, logger)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveSPAIndex(w http.ResponseWriter, r *http.Request, indexPath string, logger *log.Logger) {
	f, err := os.Open(indexPath)
	if err != nil {
		if logger != nil {
			logger.Printf("static spa: index missing %s: %v", indexPath, err)
		}
		http.Error(w, "static assets not bundled in this image", http.StatusServiceUnavailable)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "stat index", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "index.html", info.ModTime(), f)
}

// publishedHandler serves files from `dir` under urlPrefix. No SPA fallback —
// missing files 404. Used for the published meeting archive.
func publishedHandler(dir, urlPrefix string, logger *log.Logger) http.Handler {
	fileServer := http.StripPrefix(urlPrefix, http.FileServer(http.Dir(dir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// warnIfEphemeral logs a warning when a path looks like it's on an ephemeral
// filesystem (overlay, tmpfs). Best-effort; failure to detect is fine.
func warnIfEphemeral(logger *log.Logger, paths ...string) {
	for _, p := range paths {
		fsType, err := filesystemType(p)
		if err != nil {
			continue
		}
		switch fsType {
		case "overlayfs", "overlay", "tmpfs":
			logger.Printf("WARNING: %s is on %s — mount a persistent volume or data will be lost on restart", p, fsType)
		}
	}
}

func filesystemType(path string) (string, error) {
	// Linux: parse /proc/mounts for the longest matching mount point.
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	best := ""
	bestType := ""
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mp := fields[1]
		fsType := fields[2]
		if !strings.HasPrefix(path, mp) {
			continue
		}
		if len(mp) > len(best) {
			best = mp
			bestType = fsType
		}
	}
	if bestType == "" {
		return "", errors.New("no matching mount")
	}
	return bestType, nil
}
