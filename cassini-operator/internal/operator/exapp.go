package operator

import (
	"bytes"
	_ "embed"
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
//   - When AppAPI's persistent volume is mounted (APP_PERSISTENT_STORAGE),
//     data paths left unset or still at their baked image defaults are
//     redirected under it (see exAppDataPathDefault).
//
// Routing shape inside the ExApp container:
//
//   ROOT mux (wrapped by AppAPI middleware when APP_SECRET set)
//   ├── /enabled                       lifecycle (AppAPI direct call)
//   ├── /init                          lifecycle (AppAPI direct call)
//   ├── /control-panel, /control-panel/*   admin SPA static (ADMIN per manifest)
//   ├── /viewer, /viewer/*                 viewer SPA static  (USER per manifest)
//   ├── /published/*                       site archive       (USER per manifest)
//   ├── /ui/viewer.js, /ui/viewer.css       embedded viewer build (USER per manifest)
//   ├── /ui/control-panel.js                iframe bootstrap   (ADMIN per manifest)
//   ├── /img/app.svg                        navigation icon    (USER per manifest)
//   └── <BasePath>/jobs, /jobs/, /events   operator JSON API  (ADMIN per manifest)
//
// When APP_SECRET is unset the middleware is a no-op pass-through, which is
// what compose.yml expects for local dev.

const (
	envAppHost              = "APP_HOST"
	envAppPort              = "APP_PORT"
	envAppID                = "APP_ID"
	envAppVersion           = "APP_VERSION"
	envAppSecret            = "APP_SECRET"
	envAppPersistentStorage = "APP_PERSISTENT_STORAGE"
	envAppAPIRequired       = "CASSINI_APPAPI_REQUIRED"
	envControlPanelDist     = "CASSINI_CONTROL_PANEL_DIST"
	envViewerDist           = "CASSINI_VIEWER_DIST"
	envNextcloudURL         = "NEXTCLOUD_URL"
	defaultExAppBindHost    = "0.0.0.0"
	defaultExAppBindPort    = "8080"
	controlPanelURLPrefix   = "/control-panel"
	viewerURLPrefix         = "/viewer"
	publishedURLPrefix      = "/published"
)

// ExApp UI (Nextcloud navigation) wiring. AppAPI renders a top-menu entry for
// every item an ExApp registers via POST /ocs/v2.php/apps/app_api/api/v1/ui/
// top-menu; clicking it opens AppAPI's embedded page (/apps/app_api/embedded/
// <app-id>/<entry>), a bare <div id="content"> plus the scripts registered for
// that entry via .../api/v1/ui/script. Both the scripts and the entry icon are
// fetched by the browser THROUGH the AppAPI proxy, so each path served below
// must also be declared in appinfo/info.xml <routes>.
const (
	ocsUITopMenuPath = "/ocs/v2.php/apps/app_api/api/v1/ui/top-menu"
	ocsUIScriptPath  = "/ocs/v2.php/apps/app_api/api/v1/ui/script"
	ocsUIStylePath   = "/ocs/v2.php/apps/app_api/api/v1/ui/style"
	uiAssetURLPrefix = "/ui"
	navIconURLPath   = "/img/app.svg"
	// Filenames of the embedded-viewer build (vite.embedded.config.ts) under
	// <CASSINI_VIEWER_DIST>/embedded/, served at /ui/viewer.js + /ui/viewer.css.
	embeddedViewerSubdir  = "embedded"
	embeddedViewerJSFile  = "embedded.js"
	embeddedViewerCSSFile = "embedded.css"
)

// Embedded UI assets. app.svg mirrors the repo-root img/app.svg (go:embed
// cannot reach outside the module). top-menu.js is the iframe bootstrap used
// ONLY by the control-panel entry: AppAPI's embedded page CSP is
// script-src-elem 'strict-dynamic' 'nonce-…', which allows a same-host iframe
// (control-panel works fine inside one), so the admin SPA can stay iframed.
// The VIEWER entry does NOT use this bootstrap — its embedded build is served
// at /ui/viewer.js as a self-mounting IIFE (D-381), because the SPA must run
// directly on the nonce'd embedded page rather than inside a frame.
var (
	//go:embed exappassets/top-menu.js
	topMenuBootstrapJS []byte
	//go:embed exappassets/app.svg
	navIconSVG []byte
)

// uiTopMenuRegistration is the JSON body of AppAPI's OCSUi#registerExAppMenuEntry.
type uiTopMenuRegistration struct {
	Name          string `json:"name"`
	DisplayName   string `json:"displayName"`
	Icon          string `json:"icon"`
	AdminRequired int    `json:"adminRequired"`
}

// uiScriptRegistration is the JSON body of AppAPI's OCSUi#setExAppScript.
// Path is relative to the ExApp root, without the ".js" suffix — Nextcloud
// appends it when loading the script on the embedded page.
type uiScriptRegistration struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// uiStyleRegistration is the JSON body of AppAPI's OCSUi#setExAppStyle. Like
// the script registration, Path is relative to the ExApp root WITHOUT the
// ".css" suffix — AppAPI appends it when loading the stylesheet on the embedded
// page. So Path "ui/viewer" makes AppAPI request "ui/viewer.css" (registering
// "ui/viewer.css" here would 404 as "ui/viewer.css.css").
type uiStyleRegistration struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// topMenuEntries declares the navigation entries uiRegistrar registers.
// Each entry Name doubles as the URL prefix the entry's asset lives under:
//   - control-panel: /ui/control-panel.js is the iframe bootstrap that frames
//     <proxy base>/control-panel/ (the admin SPA stays iframed).
//   - viewer: /ui/viewer.js is the embedded viewer IIFE that mounts directly on
//     the AppAPI embedded page, plus /ui/viewer.css registered as its ui/style
//     (D-381). It does NOT iframe <proxy base>/viewer/.
var topMenuEntries = []uiTopMenuRegistration{
	{
		Name:          strings.TrimPrefix(viewerURLPrefix, "/"),
		DisplayName:   "Cassini",
		Icon:          strings.TrimPrefix(navIconURLPath, "/"),
		AdminRequired: 0,
	},
	{
		Name:          strings.TrimPrefix(controlPanelURLPrefix, "/"),
		DisplayName:   "Cassini Admin",
		Icon:          strings.TrimPrefix(navIconURLPath, "/"),
		AdminRequired: 1,
	},
}

// Data paths baked as ENV defaults in deployment/Dockerfile.exapp{,.cuda}.
// exAppDataPathDefault treats an env value equal to one of these as "not
// explicitly configured" so it can redirect the path under the AppAPI
// persistent volume. Keep in sync with the Dockerfiles (and with the
// effective_data_path mirror in deployment/exapp-start.sh).
const (
	imageDefaultDBPath   = "/var/lib/cassini-operator/jobs.sqlite3"
	imageDefaultWorkRoot = "/var/lib/cassini-operator/jobs"
	imageDefaultSiteRoot = "/srv/cassini-site/published"
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

// persistentStorageRoot returns the mount path of the volume AppAPI creates
// for every docker-deployed ExApp (APP_PERSISTENT_STORAGE, e.g.
// /nc_app_gocassini_data — see app_api's DockerActions::buildDefaultExAppVolume
// and buildDeployEnvs). Empty outside an AppAPI docker deploy.
func persistentStorageRoot() string {
	return strings.TrimSpace(os.Getenv(envAppPersistentStorage))
}

// exAppDataPathDefault resolves the default for one of the operator's data
// paths (job DB, work root, site root). AppAPI's docker deploy mounts exactly
// one volume — the APP_PERSISTENT_STORAGE path — so when that volume is
// present, any path left unset or still at its baked image default is
// redirected under it; otherwise the data would land on container overlayfs
// and be destroyed on every app update or recreate. Precedence:
//
//  1. an explicit env override (any value other than the baked image default)
//  2. <persistRoot>/<persistRel> when APP_PERSISTENT_STORAGE is set
//  3. the baked image default, when set in env
//  4. fallback (the repo-root derived dev default)
func exAppDataPathDefault(persistRoot, envValue, imageDefault, persistRel, fallback string) string {
	if persistRoot != "" && (envValue == "" || envValue == imageDefault) {
		return filepath.Join(persistRoot, persistRel)
	}
	if envValue != "" {
		return envValue
	}
	return fallback
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
		UIRegistrar:          c.uiRegistrar(logger),
	}
	lifecycle.Register(root)

	// ExApp UI assets referenced by the top-menu registration (uiRegistrar):
	// AppAPI's embedded page and navigation bar fetch these through the proxy.
	// Always mounted — the iframe bootstrap + icon are embedded in the binary
	// and harmless outside an AppAPI deploy; the viewer JS/CSS come from the
	// on-disk embedded build when CASSINI_VIEWER_DIST is set.
	root.Handle(uiAssetURLPrefix+"/", c.uiAssetHandler(logger))
	root.Handle(navIconURLPath, embeddedAssetHandler(navIconSVG, "image/svg+xml"))

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
	client := &http.Client{Timeout: 10 * time.Second}
	return func() {
		body := bytes.NewBufferString(`{"progress":100,"error":""}`)
		req, err := http.NewRequest(http.MethodPut, statusURL, body)
		if err != nil {
			logger.Printf("init progress: build request: %v", err)
			return
		}
		c.setAppAPIOCSHeaders(req)
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

// setAppAPIOCSHeaders sets the headers AppAPI's #[AppAPIAuth] attribute
// validates on OCS calls from an ExApp back into Nextcloud, plus the JSON
// content negotiation every such call uses.
func (c ExAppConfig) setAppAPIOCSHeaders(req *http.Request) {
	auth := base64.StdEncoding.EncodeToString([]byte(":" + c.AppSecret))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("AUTHORIZATION-APP-API", auth)
	req.Header.Set("EX-APP-ID", c.AppID)
	req.Header.Set("EX-APP-VERSION", c.AppVersion)
}

// uiRegistrar returns a callback that registers the Nextcloud navigation
// entries (topMenuEntries) and their bootstrap scripts via AppAPI's ExApp UI
// OCS API. Both endpoints upsert (insertOrUpdate server-side), so re-running
// on every enable is idempotent. Per AppAPI's ExApp lifecycle docs, UI
// registration belongs in the /enabled handler; entries persist while the app
// is disabled but AppAPI only renders them for enabled apps, so there is
// nothing to unregister on disable.
//
// Returns nil when NEXTCLOUD_URL, APP_SECRET, or APP_ID is unset — same
// dev/local-e2e escape hatch as initProgressReporter.
func (c ExAppConfig) uiRegistrar(logger *log.Logger) func() {
	if c.NextcloudURL == "" || c.AppSecret == "" || c.AppID == "" {
		return nil
	}
	nextcloudURL := strings.TrimRight(c.NextcloudURL, "/")
	type ocsCall struct {
		what    string
		url     string
		payload any
	}
	viewerEntryName := strings.TrimPrefix(viewerURLPrefix, "/")
	var calls []ocsCall
	for _, entry := range topMenuEntries {
		// Path is relative to the ExApp root WITHOUT extension; AppAPI appends
		// ".js" (script) / ".css" (style). Both resolve to "ui/<name>", served
		// by uiAssetHandler at /ui/<name>.js and /ui/<name>.css.
		assetPath := strings.TrimPrefix(uiAssetURLPrefix, "/") + "/" + entry.Name
		calls = append(calls,
			ocsCall{
				what:    fmt.Sprintf("top-menu entry %q", entry.Name),
				url:     nextcloudURL + ocsUITopMenuPath,
				payload: entry,
			},
			ocsCall{
				what: fmt.Sprintf("top-menu script for %q", entry.Name),
				url:  nextcloudURL + ocsUIScriptPath,
				payload: uiScriptRegistration{
					Type: "top_menu",
					Name: entry.Name,
					Path: assetPath,
				},
			},
		)
		// The viewer mounts its SPA directly on the embedded page (D-381), so it
		// also needs its stylesheet registered as a ui/style. The control-panel
		// entry iframes its own SPA (which carries its own CSS), so it doesn't.
		if entry.Name == viewerEntryName {
			calls = append(calls, ocsCall{
				what: fmt.Sprintf("top-menu style for %q", entry.Name),
				url:  nextcloudURL + ocsUIStylePath,
				payload: uiStyleRegistration{
					Type: "top_menu",
					Name: entry.Name,
					Path: assetPath,
				},
			})
		}
	}
	client := &http.Client{Timeout: 10 * time.Second}
	return func() {
		failures := 0
		for _, call := range calls {
			payload, err := json.Marshal(call.payload)
			if err != nil {
				logger.Printf("ERROR: exapp ui: encode %s: %v", call.what, err)
				failures++
				continue
			}
			req, err := http.NewRequest(http.MethodPost, call.url, bytes.NewReader(payload))
			if err != nil {
				logger.Printf("ERROR: exapp ui: build request for %s: %v", call.what, err)
				failures++
				continue
			}
			c.setAppAPIOCSHeaders(req)
			resp, err := client.Do(req)
			if err != nil {
				logger.Printf("ERROR: exapp ui: register %s: POST %s: %v", call.what, call.url, err)
				failures++
				continue
			}
			if resp.StatusCode >= 300 {
				snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
				logger.Printf("ERROR: exapp ui: register %s: POST %s -> %d: %s", call.what, call.url, resp.StatusCode, strings.TrimSpace(string(snippet)))
				failures++
			}
			resp.Body.Close()
		}
		if failures > 0 {
			logger.Printf("ERROR: exapp ui: %d/%d registrations failed — navigation entries may be missing; disable and re-enable the app to retry", failures, len(calls))
			return
		}
		logger.Printf("exapp ui: registered Nextcloud navigation entries: %q (users), %q (admins)", topMenuEntries[0].Name, topMenuEntries[1].Name)
	}
}

// uiAssetHandler serves the ExApp UI assets AppAPI loads on the embedded page:
//
//   - /ui/viewer.js  + /ui/viewer.css : the embedded viewer build (D-381), a
//     self-mounting IIFE + its stylesheet, read from <ViewerDist>/embedded/.
//     The viewer runs DIRECTLY on the nonce'd embedded page (no iframe).
//   - /ui/control-panel.js : the iframe bootstrap (control-panel stays iframed).
//
// Anything else under /ui/ is a 404. The proxy fetches these with the
// requesting user's access level, enforced by the matching info.xml routes.
func (c ExAppConfig) uiAssetHandler(logger *log.Logger) http.Handler {
	bootstrap := embeddedAssetHandler(topMenuBootstrapJS, "text/javascript; charset=utf-8")
	viewerEntryName := strings.TrimPrefix(viewerURLPrefix, "/")

	// Map of /ui/<name>.js -> iframe-bootstrap handler, for every entry EXCEPT
	// the viewer (whose JS is the embedded IIFE served from disk below).
	bootstrapKnown := make(map[string]bool, len(topMenuEntries))
	for _, entry := range topMenuEntries {
		if entry.Name == viewerEntryName {
			continue
		}
		bootstrapKnown[uiAssetURLPrefix+"/"+entry.Name+".js"] = true
	}

	viewerJSPath := uiAssetURLPrefix + "/" + viewerEntryName + ".js"
	viewerCSSPath := uiAssetURLPrefix + "/" + viewerEntryName + ".css"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case viewerJSPath:
			c.serveEmbeddedViewerAsset(w, r, embeddedViewerJSFile, "text/javascript; charset=utf-8", logger)
			return
		case viewerCSSPath:
			c.serveEmbeddedViewerAsset(w, r, embeddedViewerCSSFile, "text/css; charset=utf-8", logger)
			return
		}
		if bootstrapKnown[r.URL.Path] {
			bootstrap.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

// serveEmbeddedViewerAsset streams one file of the embedded viewer build from
// <ViewerDist>/embedded/<file>. The embedded build is baked into the image
// alongside the standalone viewer dist (see deployment/Dockerfile.exapp). 503
// when CASSINI_VIEWER_DIST is unset or the file is missing, mirroring how the
// SPA handler degrades when assets aren't bundled.
func (c ExAppConfig) serveEmbeddedViewerAsset(
	w http.ResponseWriter,
	r *http.Request,
	file, contentType string,
	logger *log.Logger,
) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c.ViewerDist == "" {
		if logger != nil {
			logger.Printf("ui asset: CASSINI_VIEWER_DIST unset; cannot serve %s", file)
		}
		http.Error(w, "viewer assets not bundled in this image", http.StatusServiceUnavailable)
		return
	}
	full := filepath.Join(c.ViewerDist, embeddedViewerSubdir, file)
	f, err := os.Open(full)
	if err != nil {
		if logger != nil {
			logger.Printf("ui asset: open %s: %v", full, err)
		}
		http.Error(w, "viewer assets not bundled in this image", http.StatusServiceUnavailable)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "viewer assets not bundled in this image", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, file, info.ModTime(), f)
}

// embeddedAssetHandler serves a single in-binary asset with the given
// content type. GET/HEAD only.
func embeddedAssetHandler(content []byte, contentType string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(content)
	})
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
