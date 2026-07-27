package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
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

// Per-participant access control for Nextcloud Files recordings (D-534). After
// a meeting's .opus is delivered (D-529, webdav_upload.go), this layer freezes
// its audience: it co-uploads a tiny metadata sidecar and PROPPATCHes an
// advanced-ACL grant (groupfolders `nc:acl-list`) so only the meeting's Talk
// participants — resolved at publish time — may read the .opus (and its
// sidecar). The grant is static, so later room churn does not change it: access
// is frozen at publish and editable afterwards through Nextcloud's own UI/occ.
//
// This requires the recordings root to be a groupfolder with advanced ACL and a
// default-deny floor, with the operator's owner delegated as its ACL manager —
// a one-time setup documented in docs/exapp-nextcloud-recordings-permissions.md.
// The whole layer is nil unless CASSINI_NC_ACCESS_CONTROL is enabled.

const (
	// Nextcloud permission bits (OCP\Constants): READ=1, UPDATE=2, CREATE=4,
	// DELETE=8, SHARE=16. A read-only grant governs all bits (mask=31) and
	// allows only READ (perms=1) — explicit read-only regardless of the
	// folder's default permission.
	aclMaskAll  = 31
	aclPermRead = 1

	sidecarSuffix       = ".manifest.json"
	ncFilesACLTimeout   = 60 * time.Second
	ncFilesACLMediaType = "application/xml; charset=utf-8"

	catalogSchemaVersion = "cassini.viewer.catalog.v1"
	// emptyCatalogJSON is the fail-closed body: a valid, empty catalog. Served
	// when access control is on but the caller's meetings cannot be resolved,
	// so the viewer degrades to "no meetings" rather than leaking or erroring.
	emptyCatalogJSON = `{"version":"` + catalogSchemaVersion + `","meetings":[]}`
)

// ncFilesAccessApplier writes the metadata sidecar and the advanced-ACL grants
// for one just-published meeting, acting as the recordings owner (the delegated
// ACL manager). Nil unless AppAPI is active and access control is enabled.
type ncFilesAccessApplier func(ctx context.Context, jobID, siteRoot string, mappings []aclMapping) error

// siteCatalog / siteCatalogEntry are the subset of the published catalog.json
// the sidecar is built from. The list-entry contract is {id,title,dateLabel} +
// audioPath; counts are optional.
type siteCatalog struct {
	Version  string             `json:"version"`
	Meetings []siteCatalogEntry `json:"meetings"`
}

type siteCatalogEntry struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	DateLabel        string `json:"dateLabel"`
	AudioPath        string `json:"audioPath,omitempty"`
	ArtifactPath     string `json:"artifactPath,omitempty"`
	SpeakerCount     *int   `json:"speakerCount,omitempty"`
	SegmentCount     *int   `json:"segmentCount,omitempty"`
	DigestDurationMs *int   `json:"digestDurationMs,omitempty"`
}

// ncFilesAccessApplier returns the closure, or nil when AppAPI is inactive or
// access control is disabled (the default) — in which case delivery stays the
// D-529 public archive with no ACL.
func (c ExAppConfig) ncFilesAccessApplier() ncFilesAccessApplier {
	if !c.appAPIActive() || !c.AccessControl {
		return nil
	}
	client := &http.Client{Timeout: ncFilesACLTimeout}
	return func(ctx context.Context, jobID, siteRoot string, mappings []aclMapping) error {
		opusRel := ncRecordingsRoot + "/meetings/" + jobID + ".opus"
		sidecarRel := ncRecordingsRoot + "/meetings/" + jobID + sidecarSuffix

		var errs []error
		// The .opus grant gates playback — apply it first so access is enforced
		// even if the sidecar steps fail.
		if err := c.davProppatchACL(ctx, client, ncRecordingsOwner, opusRel, mappings); err != nil {
			errs = append(errs, fmt.Errorf("acl opus: %w", err))
		}
		// The sidecar is a non-authoritative metadata accelerator; a failure
		// only forces a heavier read later, never a loss of access.
		sidecar, err := buildSidecar(siteRoot, jobID)
		if err != nil {
			errs = append(errs, fmt.Errorf("build sidecar: %w", err))
		} else {
			if err := c.davPutBytes(ctx, client, ncRecordingsOwner, sidecarRel, sidecar, "application/json"); err != nil {
				errs = append(errs, fmt.Errorf("put sidecar: %w", err))
			} else if err := c.davProppatchACL(ctx, client, ncRecordingsOwner, sidecarRel, mappings); err != nil {
				errs = append(errs, fmt.Errorf("acl sidecar: %w", err))
			}
		}
		return errors.Join(errs...)
	}
}

// buildSidecar reads the published catalog.json and returns the list entry for
// the given job as a standalone `<jobID>.manifest.json` body, with audioPath
// rewritten to the sibling .opus basename (the sidecar sits beside the .opus).
func buildSidecar(siteRoot, jobID string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(siteRoot, "catalog.json"))
	if err != nil {
		return nil, err
	}
	var cat siteCatalog
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, fmt.Errorf("parse catalog: %w", err)
	}
	want := jobID + ".opus"
	for _, e := range cat.Meetings {
		if filepath.Base(e.AudioPath) != want {
			continue
		}
		e.AudioPath = want
		e.ArtifactPath = ""
		body, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		return body, nil
	}
	return nil, fmt.Errorf("no catalog entry for %s", want)
}

func (c ExAppConfig) davPutBytes(ctx context.Context, client *http.Client, userID, relPath string, body []byte, contentType string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.davFileURL(userID, relPath), bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = int64(len(body))
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

// davProppatchACL sets the groupfolders advanced-ACL rule set on relPath: one
// read-only grant per mapping. Acts as userID (the delegated ACL manager).
func (c ExAppConfig) davProppatchACL(ctx context.Context, client *http.Client, userID, relPath string, mappings []aclMapping) error {
	body := aclListXML(mappings)
	req, err := http.NewRequestWithContext(ctx, "PROPPATCH", c.davFileURL(userID, relPath), bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Content-Type", ncFilesACLMediaType)
	req.ContentLength = int64(len(body))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	// A successful PROPPATCH returns 207 Multi-Status (or 2xx). The per-property
	// status inside the multistatus is not parsed here (best-effort delivery).
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("PROPPATCH %s -> %d", relPath, resp.StatusCode)
}

// aclListXML builds the `nc:acl-list` PROPPATCH body the groupfolders web UI
// emits (src/services/acl.ts): one <nc:acl> per mapping, read-only.
func aclListXML(mappings []aclMapping) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<d:propertyupdate xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns"><d:set><d:prop><nc:acl-list>`)
	for _, m := range mappings {
		b.WriteString(`<nc:acl><nc:acl-mapping-type>`)
		xmlEscape(&b, m.Type)
		b.WriteString(`</nc:acl-mapping-type><nc:acl-mapping-id>`)
		xmlEscape(&b, m.ID)
		b.WriteString(`</nc:acl-mapping-id><nc:acl-mask>`)
		fmt.Fprintf(&b, "%d", aclMaskAll)
		b.WriteString(`</nc:acl-mask><nc:acl-permissions>`)
		fmt.Fprintf(&b, "%d", aclPermRead)
		b.WriteString(`</nc:acl-permissions></nc:acl>`)
	}
	b.WriteString(`</nc:acl-list></d:prop></d:set></d:propertyupdate>`)
	return b.Bytes()
}

func xmlEscape(b *bytes.Buffer, s string) {
	_ = xml.EscapeText(b, []byte(s))
}

// applyNCFilesAccess freezes a just-published meeting's audience to its Talk
// participants. Best-effort and non-fatal: a failure is logged but never
// changes the already-succeeded publish. No-op unless access control is enabled
// (applyNCFilesAccessFn nil) and the job is a Talk job with grantable
// participants.
func (rt *Runtime) applyNCFilesAccess(ctx context.Context, jobID string) {
	if rt.applyNCFilesAccessFn == nil {
		return
	}
	owner, token, ok := rt.talkBindingForJob(jobID)
	if !ok {
		rt.logger.Printf("nc files access: skip id=%s (non-Talk job / no room binding)", jobID)
		return
	}
	if rt.fetchTalkParticipants == nil {
		return
	}
	mappings, err := rt.fetchTalkParticipants(ctx, owner, token)
	if err != nil {
		rt.logger.Printf("nc files access: participants lookup failed id=%s: %v", jobID, err)
		return
	}
	if len(mappings) == 0 {
		rt.logger.Printf("nc files access: no grantable participants id=%s (guests/federated only) — meeting stays manager-only", jobID)
		return
	}
	if err := rt.applyNCFilesAccessFn(ctx, jobID, rt.cfg.SiteRoot, mappings); err != nil {
		rt.logger.Printf("nc files access apply failed id=%s: %v", jobID, err)
		return
	}
	rt.logger.Printf("nc files access ok id=%s grants=%d root=%s", jobID, len(mappings), ncRecordingsRoot)
}

// serveFilteredCatalog writes the caller a catalog containing only the meetings
// they may read (D-534 read side). It fetches the authoritative catalog as the
// owner (metadata source), enumerates the meetings the caller can see with a
// per-caller PROPFIND scan of meetings/ (advanced-ACL deny-read hides the rest),
// and serves the catalog filtered to that set. Fails CLOSED: any scan error
// yields an empty catalog, never the unfiltered one.
func (c ExAppConfig) serveFilteredCatalog(ctx context.Context, w http.ResponseWriter, client *http.Client, caller string, logger *log.Logger) {
	raw, status, err := c.davGetBytes(ctx, client, ncRecordingsOwner, ncRecordingsRoot+"/catalog.json")
	if err != nil {
		if logger != nil {
			logger.Printf("nc files read: authoritative catalog fetch failed: %v", err)
		}
		http.Error(w, "Nextcloud Files unavailable", http.StatusBadGateway)
		return
	}
	if status == http.StatusNotFound {
		writeCatalogJSON(w, []byte(emptyCatalogJSON))
		return
	}
	if status < 200 || status >= 300 {
		if logger != nil {
			logger.Printf("nc files read: authoritative catalog -> %d", status)
		}
		http.Error(w, "Nextcloud Files unavailable", http.StatusBadGateway)
		return
	}

	names, perr := c.davPropfindNames(ctx, client, caller, ncRecordingsRoot+"/meetings")
	if perr != nil {
		if logger != nil {
			logger.Printf("nc files read: per-caller scan failed caller=%s: %v — serving empty (fail closed)", caller, perr)
		}
		writeCatalogJSON(w, emptyLike(raw))
		return
	}
	visible := make(map[string]bool, len(names))
	for _, n := range names {
		visible[n] = true
	}
	body, ferr := filterCatalog(raw, func(base string) bool { return visible[base] })
	if ferr != nil {
		if logger != nil {
			logger.Printf("nc files read: filter catalog failed caller=%s: %v — serving empty", caller, ferr)
		}
		writeCatalogJSON(w, emptyLike(raw))
		return
	}
	writeCatalogJSON(w, body)
}

func writeCatalogJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(ncFilesSourceHeader, ncFilesSourceValue)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// emptyLike returns the catalog with an empty meetings list, preserving the
// version/top-level shape of raw; falls back to the constant empty catalog.
func emptyLike(raw []byte) []byte {
	if body, err := filterCatalog(raw, func(string) bool { return false }); err == nil {
		return body
	}
	return []byte(emptyCatalogJSON)
}

// filterCatalog keeps only the meetings whose .opus basename passes keep,
// preserving every other field of the catalog and of each kept entry (it
// filters raw messages, so unknown fields survive).
func filterCatalog(raw []byte, keep func(opusBase string) bool) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	var meetings []json.RawMessage
	if m, ok := top["meetings"]; ok {
		if err := json.Unmarshal(m, &meetings); err != nil {
			return nil, err
		}
	}
	kept := make([]json.RawMessage, 0, len(meetings))
	for _, mr := range meetings {
		var e struct {
			AudioPath    string `json:"audioPath"`
			ArtifactPath string `json:"artifactPath"`
		}
		_ = json.Unmarshal(mr, &e)
		ref := e.AudioPath
		if ref == "" {
			ref = e.ArtifactPath
		}
		if keep(path.Base(ref)) {
			kept = append(kept, mr)
		}
	}
	mj, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	top["meetings"] = mj
	return json.Marshal(top)
}

func (c ExAppConfig) davGetBytes(ctx context.Context, client *http.Client, userID, relPath string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.davFileURL(userID, relPath), nil)
	if err != nil {
		return nil, 0, err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer drainClose(resp.Body)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// davPropfindNames lists the immediate children of relDir as userID (Depth: 1)
// and returns the .opus basenames the user can see. Advanced-ACL deny-read
// hides paths the user lacks read on, so the result is naturally access-scoped.
// A 404 (the collection is not visible to the user) yields an empty list, not
// an error.
func (c ExAppConfig) davPropfindNames(ctx context.Context, client *http.Client, userID, relDir string) ([]string, error) {
	// Request only <d:resourcetype/>: the href (all we need) is always returned,
	// and a minimal prop set keeps each child's response element to a few
	// hundred bytes instead of the multi-KiB allprops default.
	reqBody := []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>`)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", c.davFileURL(userID, relDir), bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", ncFilesACLMediaType)
	req.ContentLength = int64(len(reqBody))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("PROPFIND %s -> %d", relDir, resp.StatusCode)
	}
	// Read with an explicit cap and detect truncation: a silently truncated
	// multistatus would fail to parse and blank the listing for every caller
	// (fail-closed but self-inflicted), so surface it loudly instead.
	const maxMultistatus = 64 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMultistatus+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxMultistatus {
		return nil, fmt.Errorf("PROPFIND %s: multistatus exceeds %d bytes (too many meetings?)", relDir, maxMultistatus)
	}
	var ms struct {
		Responses []struct {
			Href string `xml:"href"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(body, &ms); err != nil {
		return nil, fmt.Errorf("parse multistatus: %w", err)
	}
	out := make([]string, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		href := strings.TrimRight(r.Href, "/")
		base := path.Base(href)
		if decoded, derr := url.PathUnescape(base); derr == nil {
			base = decoded
		}
		if strings.HasSuffix(base, ".opus") {
			out = append(out, base)
		}
	}
	return out, nil
}
