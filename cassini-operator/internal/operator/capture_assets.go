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
// uploads it after the call. The companion PHP app injects the payload on
// Talk's page; the ExApp continues to serve the payload build and its worker.
//
//	/ui/capture-payload.js  finds the outgoing audio sender,
//	                        records it, uploads afterwards
//	/ui/capture-worker.js   encoded-transform (RTP timing anchors) + OPFS writes
//
// They are built by cassini-app's scripts/build-capture.mjs into
// <CASSINI_VIEWER_DIST>/capture/ and served from there, alongside the embedded
// SPA bundle the same dist already carries.
const (
	captureSubdir = "capture"

	envSourceCaptureEnabled = "CASSINI_SOURCE_CAPTURE"

	capturePayloadFile = "capture-payload.js"
	captureWorkerFile  = "capture-worker.js"
)

// captureAssetFiles maps the served /ui/ path suffix to its file in the dist.
var captureAssetFiles = map[string]string{
	capturePayloadFile: capturePayloadFile,
	captureWorkerFile:  captureWorkerFile,
}

// sourceCaptureEnabled reports whether this installation collects
// participant-captured audio at all.
//
// This is the containment boundary for the whole feature, and it is separate
// from CASSINI_SOURCE_AUDIO_INGEST (which only decides whether collected audio
// reaches a transcript). With this off, the browser assets 404 and the upload
// endpoint refuses: the injected payload is told no, no capture asset loads,
// nothing is stored, and no disk can be filled. A user opting in
// client-side achieves nothing an administrator has not allowed.
//
// Off by default. The known residual risks — consent recorded per browser
// origin rather than per Nextcloud account, and an upload endpoint with no
// per-participant quota — are acceptable for a deployment whose operator chose
// to run this prototype, and are not acceptable for one that merely upgraded.
func sourceCaptureEnabled() bool {
	enabled, err := parseBoolEnv(envSourceCaptureEnabled)
	return err == nil && enabled
}

// serveCaptureAsset streams one built capture bundle from <dist>/capture/.
func (c ExAppConfig) serveCaptureAsset(w http.ResponseWriter, r *http.Request, file string, logger *log.Logger) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sourceCaptureEnabled() {
		// 404 rather than 403: to a browser this is simply an installation
		// with no such feature. The companion's initial state independently
		// prevents the payload from instrumenting Talk.
		http.NotFound(w, r)
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
