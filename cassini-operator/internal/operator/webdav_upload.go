package operator

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"cassini-operator/internal/operator/appapi"
)

// Nextcloud-native delivery stores each published .opus in the dedicated
// account's private Files directory. The publish sink creates direct Files
// shares; the read proxy resolves the caller's current shares and relays DAV
// reads as that caller. There is no remote catalog or automatic archive mirror.
// Talk receives status callbacks; Cassini does not use Talk's /store endpoint.
const (
	// The stable non-human account owns recordings and creates their shares.
	ncRecordingsOwner = "cassini"

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

// errDAVPreconditionFailed is a conditional PUT Nextcloud refused because the
// file changed after its ETag was read (412). The caller re-reads and retries;
// it is contention, not a failure of the request (D-737).
var errDAVPreconditionFailed = errors.New("the file changed since it was read")

func (c ExAppConfig) davPutFileStatus(ctx context.Context, client *http.Client, userID, relPath, localPath, contentType string) (int, error) {
	status, _, err := c.davPutFileIfMatch(ctx, client, userID, relPath, localPath, contentType, "")
	return status, err
}

// davPutFileIfMatch uploads localPath as davPutFileStatus does — stamping
// OC-Checksum, which search's backfill reads back as the delivered digest — and,
// when ifMatch is non-empty, only if the stored file still carries that ETag. A
// refusal wraps errDAVPreconditionFailed. On success it answers the new ETag
// when Nextcloud reports one.
func (c ExAppConfig) davPutFileIfMatch(ctx context.Context, client *http.Client, userID, relPath, localPath, contentType, ifMatch string) (int, string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return 0, "", err
	}
	digest, err := fileSHA256(localPath)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.davFileURL(userID, relPath), f)
	if err != nil {
		return 0, "", err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("OC-Checksum", "SHA256:"+digest)
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	req.ContentLength = info.Size()
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusPreconditionFailed && ifMatch != "" {
		return resp.StatusCode, "", fmt.Errorf("PUT %s: %w", relPath, errDAVPreconditionFailed)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, strings.TrimSpace(resp.Header.Get("ETag")), nil
	}
	return resp.StatusCode, "", fmt.Errorf("PUT %s -> %d", relPath, resp.StatusCode)
}

// davDownloadFile streams relPath, read as userID, into destPath, and answers
// the sha256 and the length of what it wrote. Any non-2xx is an error; status
// says which, so a caller can tell absence (404) from failure without parsing a
// message.
//
// Streamed rather than read into memory: a long meeting is tens of megabytes.
// When limit is positive it reads one byte past it, so an oversized recording is
// refused rather than silently truncated; zero means no limit. destPath is
// created 0600 and truncated, so a retry into the same path starts clean.
//
// An empty body is NOT refused here. The publish path has to be able to fetch
// the empty leaf an interrupted first publish leaves, in order to replace it;
// callers for whom an empty recording is an error say so themselves
// (stageRecording).
func (c ExAppConfig) davDownloadFile(ctx context.Context, client *http.Client, userID, relPath, destPath string, limit int64, expectedETag ...string) (digest string, written int64, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.davFileURL(userID, relPath), nil)
	if err != nil {
		return "", 0, 0, err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	if len(expectedETag) > 0 {
		req.Header.Set("If-Match", expectedETag[0])
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, 0, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, resp.StatusCode, fmt.Errorf("GET %s -> %d", relPath, resp.StatusCode)
	}
	file, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", 0, resp.StatusCode, err
	}
	defer file.Close()
	var body io.Reader = resp.Body
	if limit > 0 {
		body = io.LimitReader(resp.Body, limit+1)
	}
	sum := sha256.New()
	written, err = io.Copy(io.MultiWriter(file, sum), body)
	if err != nil {
		return "", written, resp.StatusCode, fmt.Errorf("read %s: %w", relPath, err)
	}
	if limit > 0 && written > limit {
		return "", written, resp.StatusCode, fmt.Errorf("GET %s: the recording is larger than the %d MiB limit", relPath, limit>>20)
	}
	if err := file.Sync(); err != nil {
		return "", written, resp.StatusCode, err
	}
	return hex.EncodeToString(sum.Sum(nil)), written, resp.StatusCode, file.Close()
}

// ncFilesProxy serves the installed Nextcloud archive as the current caller.
// With the local static sink, the file handler serves the local export instead.
func (c ExAppConfig) ncFilesProxy(logger *log.Logger, search searchDeps) ncFilesProxyFunc {
	if !c.appAPIActive() {
		return nil
	}
	// No overall client timeout: audio bodies stream to the caller and are
	// bounded by the request context; a hung upstream is bounded on headers.
	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: ncFilesProxyHeadersTTL}}
	return func(w http.ResponseWriter, r *http.Request, relPath string) bool {
		if c.PublishSink != publishSinkNextcloudFiles {
			return false
		}
		// A missing AppAPI user is a transport failure, not an empty list.
		caller := appapi.UserID(r.Context())
		if caller == "" {
			if logger != nil {
				logger.Printf("nc files read: missing caller identity path=%s — failing closed", relPath)
			}
			switch relPath {
			case "catalog.json", meetingsListPath, searchURLPath:
				writeJSONError(w, http.StatusBadGateway,
					"your Nextcloud identity did not reach Cassini; this is not an empty result")
			default:
				http.NotFound(w, r)
			}
			return true
		}
		readAs := caller

		if relPath == "catalog.json" {
			c.serveFilteredCatalog(r.Context(), w, client, caller, logger)
			return true
		}
		if relPath == meetingsListPath {
			c.serveMeetingsList(r.Context(), w, r, client, caller, search, logger)
			return true
		}
		if relPath == searchURLPath {
			c.serveSearch(r.Context(), w, r, client, caller, search, logger)
			return true
		}
		if !strings.HasPrefix(relPath, "meetings/") || !strings.HasSuffix(relPath, ".opus") {
			http.NotFound(w, r)
			return true
		}
		davRelPath, resolveErr := c.recipientRecordingPath(r.Context(), client, caller, path.Base(relPath), c.meetingMetadata)
		if resolveErr != nil {
			if errors.Is(resolveErr, errRecordingNotShared) {
				http.NotFound(w, r)
			} else {
				http.Error(w, "Nextcloud shares unavailable", http.StatusBadGateway)
			}
			return true
		}
		davURL := c.davFileURL(readAs, davRelPath)
		req, err := http.NewRequestWithContext(r.Context(), r.Method, davURL, nil)
		if err != nil {
			if logger != nil {
				logger.Printf("nc files read: build request path=%s: %v", relPath, err)
			}
			http.Error(w, "Nextcloud Files request failed", http.StatusInternalServerError)
			return true
		}
		c.setAppAPIDAVHeadersForUser(req, readAs)
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
