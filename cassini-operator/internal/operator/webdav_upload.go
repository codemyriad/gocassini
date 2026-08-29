package operator

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"cassini-operator/internal/operator/appapi"
)

// Nextcloud-native delivery of the published meeting archive (D-529). A
// published meeting reaches Nextcloud Files over WebDAV — the clean portable
// `.opus` plus its entry in the `catalog.json` index — so the artefacts live in
// NC Files (quota-counted, backed up, natively shareable) instead of the
// ExApp's private volume, and the viewer is fed from there.
//
// Delivery is per-meeting and happens inside the publish job, in
// publish_sink_nextcloud.go. This file holds the DAV primitives that sink is
// built from, plus the read proxy that serves the archive back.
//
// There is deliberately NO automatic whole-archive mirror (D-613). One used to
// run on the AppAPI enabled edge: it re-PUT every `.opus` under the local site
// root unconditionally, under a single cumulative 120s budget, so past a
// certain archive size it died before the catalog PUT and pinned the remote
// catalog forever (D-540); it also fired only on that edge, never on a plain
// restart, despite its name (D-541). Since delivery became a mandatory publish
// stage (D-549) and the local site root stopped being written at all (D-550),
// its only remaining job was converging an archive published by an older
// local-only version. That is a one-time migration, so it is now an explicit
// one — `cassini-operator backfill-nc-files`, run by hand (nc_backfill.go).
//
// POLICY: Cassini never uses Talk's recording `/store` endpoint, for any file
// (D-551). A meeting reaches Nextcloud exactly once, as the published `.opus`
// under the canonical recordings root — because that tree is the only one
// covered by the per-file ACL model the read proxy enforces (D-521). Anything
// filed into a user's Talk attachment folder would be an unmanaged parallel
// copy of the same meeting, outside the catalog and outside access control.
// Talk receives status callbacks only. (A secondary consequence: `/store`'s
// format allow-list rejects `.opus` anyway — it accepts only
// ogg/ogv/mp4/webm/mkv — whereas a plain WebDAV PUT accepts any bytes and
// Nextcloud maps `.opus` to audio/ogg for playback.)
//
// FIRST PASS (D-529) scope, deliberately minimal:
//   - owner = the dedicated `cassini` service account, provisioned on the
//     AppAPI enabled edge (D-532).
//   - layout mirrors the published site 1:1 under a hard-coded canonical root:
//     <root>/catalog.json and <root>/meetings/<id>.opus. Room-namespaced
//     (<root>/<room>/<id>.opus) layout and a configurable root come later.
//   - Access control is now unconditional (D-554): the operator writes as the
//     single owner and serves each caller as themselves (D-530 added the
//     per-user/per-group access model on top of this topology).
const (
	// ncRecordingsOwner is the Nextcloud account that owns the canonical
	// recordings tree — it performs the WebDAV writes, manages each recording's
	// ACL, and is the identity the read proxy reads the authoritative catalog
	// as (D-532).
	//
	// A dedicated service account rather than the instance administrator, for
	// three reasons: recordings get a stable non-human owner that does not
	// change when staff do; the account that holds every recording is not also
	// able to reconfigure the instance (provisioning acts as the admin instead,
	// see nc_provision.go); and nothing assumes an administrator is literally
	// called "admin", which is true only by convention.
	//
	// The tree lives in a group folder, so this names the identity the operator
	// acts as, not a home directory — the recordings are in shared group-folder
	// storage either way. That is why changing it needs no data migration.
	ncRecordingsOwner = "cassini"
	// ncRecordingsRoot is the canonical recordings root inside the owner's
	// Files (relative to the user's WebDAV home). Hard-coded for now (D-529).
	ncRecordingsRoot = "Cassini/Recordings"
	// ncRecordingsEveryoneGroup is the virtual all-users group supplied by the
	// Nextcloud Everyone Group app. It gives every account a read-only Team-folder
	// mount from account creation; per-file ACLs deny it for private meetings and
	// allow only participants, preventing the container grant from leaking files.
	ncRecordingsEveryoneGroup = "everyone"
	// ncLegacyRecordingsViewerGroup identifies the static group used before the
	// virtual group migration. It is read only to translate existing ACLs safely.
	ncLegacyRecordingsViewerGroup = "recording-viewers"

	ncFilesUploadTimeout   = 120 * time.Second
	ncFilesProxyHeadersTTL = 30 * time.Second
	ncFilesSourceHeader    = "X-Cassini-Meeting-Source"
	ncFilesSourceValue     = "nextcloud-files"
)

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

// davPutEmpty creates relPath as a zero-byte file and reports the status.
//
// This is the first half of "rule the object before it has any content in it"
// (D-594). There is no atomic create-with-ACL for a file — PROPPATCH on a path
// that does not exist is a 404, no PUT header carries rules, and groupfolders
// exposes no per-path OCS route — so the only way to get an ACL onto a leaf
// before its audio is to create the leaf empty, PROPPATCH it, and then fill it.
// An overwriting PUT preserves the file's fileid, and groupfolders ACL rows are
// keyed by fileid, so the rules written here survive every later overwrite.
//
// What is exposed in the window this leaves open is the file's name, a length of
// zero, and its mtime — never audio.
func (c ExAppConfig) davPutEmpty(ctx context.Context, client *http.Client, userID, relPath, contentType string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.davFileURL(userID, relPath), bytes.NewReader(nil))
	if err != nil {
		return 0, err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = 0
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("PUT (empty) %s -> %d", relPath, resp.StatusCode)
}

// davDelete removes relPath. A 404 is success: the caller wanted it gone.
//
// CAUTION: deleting a leaf in a group folder does not destroy it. The bytes move
// to the Team-folder trash, where they stay reachable to exactly the accounts the
// leaf's rules allowed — and a leaf with NO rules is readable and listable there
// by every account, from its own trashbin. So a DELETE issued to remediate an
// unprotected recording relocates the exposure rather than ending it. Every call
// site must PROPPATCH a deny onto the leaf first; see repairUnprotectedLeaf.
func (c ExAppConfig) davDelete(ctx context.Context, client *http.Client, userID, relPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.davFileURL(userID, relPath), nil)
	if err != nil {
		return err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return nil
	}
	return fmt.Errorf("DELETE %s -> %d", relPath, resp.StatusCode)
}

func (c ExAppConfig) davPutFileStatus(ctx context.Context, client *http.Client, userID, relPath, localPath, contentType string) (int, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.davFileURL(userID, relPath), f)
	if err != nil {
		return 0, err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = info.Size()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("PUT %s -> %d", relPath, resp.StatusCode)
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
		// Per-user access control (D-534, unconditional since D-554): serve each
		// caller only what they may read. The caller identity comes from the
		// AppAPI-verified request; these routes are USER-gated, so it is always
		// present. There is no owner-identity path left — serving the archive as
		// the owner is precisely the org-wide behaviour D-521 retired.
		caller := appapi.UserID(r.Context())
		if caller == "" {
			if logger != nil {
				logger.Printf("nc files read: missing caller identity path=%s — failing closed", relPath)
			}
			if relPath == "catalog.json" {
				writeCatalogJSON(w, []byte(emptyCatalogJSON))
			} else {
				http.NotFound(w, r)
			}
			return true
		}
		if relPath == "catalog.json" {
			// The list is built per caller (authoritative catalog filtered
			// by the caller's own PROPFIND scan), not streamed as-is.
			c.serveFilteredCatalog(r.Context(), w, client, caller, logger)
			return true
		}
		// meetings/<id>.opus: fetch AS the caller so Nextcloud enforces the
		// per-file ACL — a non-readable meeting 404s and never leaks.
		davURL := c.davFileURL(caller, ncRecordingsRoot+"/"+strings.TrimPrefix(relPath, "/"))
		req, err := http.NewRequestWithContext(r.Context(), r.Method, davURL, nil)
		if err != nil {
			if logger != nil {
				logger.Printf("nc files read: build request path=%s: %v", relPath, err)
			}
			http.Error(w, "Nextcloud Files request failed", http.StatusInternalServerError)
			return true
		}
		c.setAppAPIDAVHeadersForUser(req, caller)
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
			if relPath == "catalog.json" {
				// The catalog changes after every publish. Nextcloud's AppAPI proxy
				// otherwise gives this response a one-hour browser freshness window,
				// hiding newly published meetings even after reopening the viewer.
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Content-Type", "application/json")
			}
			w.Header().Set(ncFilesSourceHeader, ncFilesSourceValue)
			w.WriteHeader(resp.StatusCode)
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, resp.Body)
			}
		case http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden:
			// Denied and absent must look the same to the caller. The whole
			// point of the access model is that a recording you may not read
			// never reveals that it exists — a 403 here would leak exactly
			// that, and a 502 would suggest an outage that is not happening
			// (D-521). Logged so the operator can still tell them apart.
			if resp.StatusCode != http.StatusNotFound && logger != nil {
				logger.Printf("nc files read: denied path=%s -> %d (served as 404)", relPath, resp.StatusCode)
			}
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
