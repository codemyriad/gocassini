package operator

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Source-capture browser assets.
//
// Cassini's recorder subscribes through the SFU, so the audio it stores is
// whatever survived each participant's uplink; on a bad connection the words
// are simply not there. Source capture records the same signal in the
// participant's own browser, before Opus encoding and before the network, and
// uploads it after the call. These three files are how that code reaches the
// browser.
//
//	/ui/capture-sw.js       service worker; registered by the Cassini page at
//	                        the Talk call scopes, appends the payload to Talk's
//	                        own bundle
//	/ui/capture-payload.js  the appended code: finds the outgoing audio sender,
//	                        records it, uploads afterwards
//	/ui/capture-worker.js   encoded-transform (RTP timing anchors) + OPFS writes
//
// They are built by cassini-app's scripts/build-capture.mjs into
// <CASSINI_VIEWER_DIST>/capture/ and served from there, alongside the embedded
// SPA bundle the same dist already carries.
const (
	captureSubdir = "capture"

	captureSWFile      = "capture-sw.js"
	capturePayloadFile = "capture-payload.js"
	captureWorkerFile  = "capture-worker.js"
)

// captureAssetFiles maps the served /ui/ path suffix to its file in the dist.
var captureAssetFiles = map[string]string{
	captureSWFile:      captureSWFile,
	capturePayloadFile: capturePayloadFile,
	captureWorkerFile:  captureWorkerFile,
}

// serveCaptureAsset streams one built capture bundle from <dist>/capture/.
//
// The service worker gets one extra response header, and the entire feature
// depends on it. A service worker may only claim a scope at or below its own
// script path unless `Service-Worker-Allowed` widens the maximum, and our
// script is served from deep inside the AppAPI proxy path while the scopes we
// need are Talk's call pages. AppAPI's ExAppProxyController::createProxyResponse
// copies every response header except its own auth set, so the header survives
// the proxy hop to the browser. Nextcloud core relies on exactly this mechanism
// for its own Files preview worker (apps/files/lib/Controller/ApiController.php
// sets Service-Worker-Allowed: "/").
//
// The header only raises the CEILING on what may be claimed. The page registers
// at the two narrow Talk call scopes and never at "/", which is what keeps this
// from displacing the root-scoped worker core registers.
func (c ExAppConfig) serveCaptureAsset(w http.ResponseWriter, r *http.Request, file string, logger *log.Logger) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c.ViewerDist == "" {
		if logger != nil {
			logger.Printf("capture asset: %s unset; cannot serve %s", envViewerDist, file)
		}
		http.Error(w, "capture assets not bundled in this image", http.StatusServiceUnavailable)
		return
	}
	full := filepath.Join(c.ViewerDist, captureSubdir, file)
	f, err := os.Open(full)
	if err != nil {
		if logger != nil {
			logger.Printf("capture asset: open %s: %v", full, err)
		}
		http.Error(w, "capture assets not bundled in this image", http.StatusServiceUnavailable)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "capture assets not bundled in this image", http.StatusServiceUnavailable)
		return
	}
	if file == captureSWFile {
		w.Header().Set("Service-Worker-Allowed", "/")
		// A stale service worker is worse than a slow one: it keeps rewriting
		// Talk's bundle with a payload the server no longer agrees with.
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	http.ServeContent(w, r, file, info.ModTime(), f)
}

// captureAssetFileFor returns the dist file for a /ui/ request path, and
// whether the path names a capture asset at all.
func captureAssetFileFor(urlPath string) (string, bool) {
	name := strings.TrimPrefix(urlPath, uiAssetURLPrefix+"/")
	if strings.Contains(name, "/") {
		return "", false
	}
	file, ok := captureAssetFiles[name]
	return file, ok
}
