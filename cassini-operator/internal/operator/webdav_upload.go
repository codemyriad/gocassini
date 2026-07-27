package operator

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Nextcloud-native delivery of the published meeting archive (D-529). On the
// AppAPI enabled edge and after every successful publish, the operator mirrors
// the browsable archive into Nextcloud Files over WebDAV — the clean portable `.opus` per meeting plus the
// `catalog.json` index — so the artefacts live in NC Files (quota-counted,
// backed up, natively shareable) instead of only the ExApp's private volume,
// and the viewer can be fed from there. Talk's recording `/store` endpoint is
// intentionally NOT used: its format allow-list rejects `.opus` (it accepts
// only ogg/ogv/mp4/webm/mkv), whereas a plain WebDAV PUT accepts any bytes and
// Nextcloud maps `.opus` to audio/ogg for playback.
//
// FIRST PASS (D-529) scope, deliberately minimal:
//   - owner = the Nextcloud admin (hard-coded). A dedicated `cassini` service
//     account is tracked separately (D-532); swapping ncRecordingsOwner is the
//     only code change needed.
//   - layout mirrors the published site 1:1 under a hard-coded canonical root:
//     <root>/catalog.json and <root>/meetings/<id>.opus. Room-namespaced
//     (<root>/<room>/<id>.opus) layout and a configurable root come later.
//   - PUBLIC: access control is out of scope; the operator reads/writes as the
//     single owner and serves to any authenticated caller (D-530 adds the
//     per-user/per-group access model on top of this topology).
const (
	// ncRecordingsOwner is the Nextcloud user whose Files hold the canonical
	// recordings tree, impersonated for both the WebDAV PUT (delivery) and the
	// read proxy (serving). Hard-coded to admin for the first pass — see D-532.
	ncRecordingsOwner = "admin"
	// ncRecordingsRoot is the canonical recordings root inside the owner's
	// Files (relative to the user's WebDAV home). Hard-coded for now (D-529).
	ncRecordingsRoot = "Cassini/Recordings"

	ncFilesUploadTimeout   = 120 * time.Second
	ncFilesProxyHeadersTTL = 30 * time.Second
	ncFilesSourceHeader    = "X-Cassini-Meeting-Source"
	ncFilesSourceValue     = "nextcloud-files"
)

// ncFilesUploader mirrors the complete published archive into the owner's
// Nextcloud Files. Every .opus is uploaded before catalog.json, so the Files
// catalog never points at an archive object that this sync has not delivered.
// Best-effort at the runtime boundary — a failure must never fail the publish.
// Nil outside an AppAPI deployment (no NextcloudURL/APP_SECRET/APP_ID).
type ncFilesUploader func(ctx context.Context, siteRoot string) error

// ncFilesProxyFunc serves a published archive path (catalog.json or
// meetings/<id>.opus) from the owner's Nextcloud Files, forwarding the inbound
// Range header and relaying status/headers/body. In an AppAPI deployment Files
// is authoritative: the function always handles the request, including misses
// and upstream failures. Nil outside an AppAPI deployment, where handlers keep
// serving the local site.
type ncFilesProxyFunc func(w http.ResponseWriter, r *http.Request, relPath string) bool

// davFileURL builds the user-WebDAV URL for a path relative to the user's home,
// escaping each segment while preserving the separators.
func (c ExAppConfig) davFileURL(userID, relPath string) string {
	base := strings.TrimRight(c.NextcloudURL, "/")
	segs := []string{"remote.php", "dav", "files", userID}
	for _, s := range strings.Split(strings.Trim(relPath, "/"), "/") {
		if s == "" {
			continue
		}
		segs = append(segs, s)
	}
	escaped := make([]string, 0, len(segs))
	for _, s := range segs {
		escaped = append(escaped, url.PathEscape(s))
	}
	return base + "/" + strings.Join(escaped, "/")
}

// setAppAPIDAVHeadersForUser sets the AppAPI act-as-user auth headers for a
// WebDAV call. It uses the same credential scheme as AppAPI's own outbound
// requests (base64("<userId>:<secret>")), but without OCS JSON content
// negotiation; DAV callers set Content-Type for the resource body.
func (c ExAppConfig) setAppAPIDAVHeadersForUser(req *http.Request, userID string) {
	auth := base64.StdEncoding.EncodeToString([]byte(userID + ":" + c.AppSecret))
	req.Header.Set("AUTHORIZATION-APP-API", auth)
	req.Header.Set("EX-APP-ID", c.AppID)
	req.Header.Set("EX-APP-VERSION", c.AppVersion)
	if c.AAVersion != "" {
		req.Header.Set("AA-VERSION", c.AAVersion)
	}
}

func (c ExAppConfig) appAPIActive() bool {
	return c.NextcloudURL != "" && c.AppSecret != "" && c.AppID != ""
}

// ncFilesUploader returns the delivery closure, or nil when the ExApp env is
// absent (dev/standalone deploys simply skip NC-Files delivery).
func (c ExAppConfig) ncFilesUploader() ncFilesUploader {
	if !c.appAPIActive() {
		return nil
	}
	client := &http.Client{Timeout: ncFilesUploadTimeout}
	return func(ctx context.Context, siteRoot string) error {
		catalogLocal := filepath.Join(siteRoot, "catalog.json")
		if _, err := os.Stat(catalogLocal); err != nil {
			return fmt.Errorf("stat catalog: %w", err)
		}
		meetingsLocal := filepath.Join(siteRoot, "meetings")
		entries, err := os.ReadDir(meetingsLocal)
		if err != nil {
			return fmt.Errorf("read meetings: %w", err)
		}

		// Ensure the canonical collections exist (idempotent). Created parent
		// first so each MKCOL's parent is present.
		for _, dir := range []string{
			path.Dir(ncRecordingsRoot),     // Cassini
			ncRecordingsRoot,               // Cassini/Recordings
			ncRecordingsRoot + "/meetings", // Cassini/Recordings/meetings
		} {
			if dir == "." || dir == "" {
				continue
			}
			if err := c.davMkcol(ctx, client, ncRecordingsOwner, dir); err != nil {
				return fmt.Errorf("mkcol %s: %w", dir, err)
			}
		}

		// Mirror the complete archive, not only the job that triggered this
		// publish. This repairs prior failed deliveries and makes upgrades from
		// a local-only archive converge on the next sync. os.ReadDir is sorted
		// by filename, keeping request order deterministic.
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".opus" {
				continue
			}
			opusLocal := filepath.Join(meetingsLocal, entry.Name())
			opusRemote := ncRecordingsRoot + "/meetings/" + entry.Name()
			if err := c.davPutFile(ctx, client, ncRecordingsOwner, opusRemote, opusLocal, "audio/ogg"); err != nil {
				return fmt.Errorf("put opus %s: %w", entry.Name(), err)
			}
		}

		// Publish the whole catalog last. If any .opus PUT fails, readers retain
		// the previous Files catalog instead of receiving references to missing
		// meeting objects.
		if err := c.davPutFile(ctx, client, ncRecordingsOwner, ncRecordingsRoot+"/catalog.json", catalogLocal, "application/json"); err != nil {
			return fmt.Errorf("put catalog: %w", err)
		}
		return nil
	}
}

func (rt *Runtime) syncNCFiles(ctx context.Context) error {
	rt.ncFilesSyncMu.Lock()
	defer rt.ncFilesSyncMu.Unlock()
	return rt.uploadToNCFiles(ctx, rt.cfg.SiteRoot)
}

// syncNCFilesOnStartup converges an archive that was published before the
// current process started. This matters on upgrades from a local-only version:
// the existing viewer catalog becomes available from Files without requiring a
// brand-new recording. The AppAPI enabled callback runs this asynchronously
// after registration is enabled, when outbound act-as-user DAV auth is valid.
func (rt *Runtime) syncNCFilesOnStartup() {
	catalogPath := filepath.Join(rt.cfg.SiteRoot, "catalog.json")
	if _, err := os.Stat(catalogPath); err != nil {
		if !os.IsNotExist(err) {
			rt.logger.Printf("nc files startup sync skipped: stat catalog: %v", err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(rt.ctx, ncFilesUploadTimeout)
	defer cancel()
	if err := rt.syncNCFiles(ctx); err != nil {
		rt.logger.Printf("nc files startup sync failed: %v", err)
		return
	}
	rt.logger.Printf("nc files startup sync ok root=%s", ncRecordingsRoot)
}

func (c ExAppConfig) davMkcol(ctx context.Context, client *http.Client, userID, relDir string) error {
	req, err := http.NewRequestWithContext(ctx, "MKCOL", c.davFileURL(userID, relDir), nil)
	if err != nil {
		return err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	// 201 Created on success; 405 Method Not Allowed when the collection
	// already exists — both mean "the directory is there".
	if resp.StatusCode == http.StatusMethodNotAllowed || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return nil
	}
	return fmt.Errorf("MKCOL %s -> %d", relDir, resp.StatusCode)
}

func (c ExAppConfig) davPutFile(ctx context.Context, client *http.Client, userID, relPath, localPath, contentType string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.davFileURL(userID, relPath), f)
	if err != nil {
		return err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = info.Size()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("PUT %s -> %d", relPath, resp.StatusCode)
}

// ncFilesProxy returns the read-proxy closure, or nil when the ExApp env is
// absent (dev/standalone serve straight from local disk as before).
func (c ExAppConfig) ncFilesProxy(logger *log.Logger) ncFilesProxyFunc {
	if !c.appAPIActive() {
		return nil
	}
	// No overall client timeout: audio bodies stream to the caller and are
	// bounded by the request context; a hung upstream is bounded on headers.
	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: ncFilesProxyHeadersTTL}}
	return func(w http.ResponseWriter, r *http.Request, relPath string) bool {
		davURL := c.davFileURL(ncRecordingsOwner, ncRecordingsRoot+"/"+strings.TrimPrefix(relPath, "/"))
		req, err := http.NewRequestWithContext(r.Context(), r.Method, davURL, nil)
		if err != nil {
			if logger != nil {
				logger.Printf("nc files read: build request path=%s: %v", relPath, err)
			}
			http.Error(w, "Nextcloud Files request failed", http.StatusInternalServerError)
			return true
		}
		c.setAppAPIDAVHeadersForUser(req, ncRecordingsOwner)
		if rng := r.Header.Get("Range"); rng != "" {
			req.Header.Set("Range", rng)
		}
		resp, err := client.Do(req)
		if err != nil {
			if logger != nil {
				logger.Printf("nc files read: GET path=%s: %v", relPath, err)
			}
			http.Error(w, "Nextcloud Files unavailable", http.StatusBadGateway)
			return true
		}
		defer drainClose(resp.Body)

		switch resp.StatusCode {
		case http.StatusOK, http.StatusPartialContent:
			for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
				if v := resp.Header.Get(h); v != "" {
					w.Header().Set(h, v)
				}
			}
			w.Header().Set(ncFilesSourceHeader, ncFilesSourceValue)
			w.WriteHeader(resp.StatusCode)
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, resp.Body)
			}
		case http.StatusNotFound:
			http.NotFound(w, r)
		case http.StatusRequestedRangeNotSatisfiable:
			if v := resp.Header.Get("Content-Range"); v != "" {
				w.Header().Set("Content-Range", v)
			}
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		default:
			if logger != nil {
				logger.Printf("nc files read: GET path=%s -> %d", relPath, resp.StatusCode)
			}
			http.Error(w, "Nextcloud Files unavailable", http.StatusBadGateway)
		}
		return true
	}
}

func drainClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<16))
	_ = body.Close()
}
