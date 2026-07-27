package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
