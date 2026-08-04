package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
)

// The nextcloud-files sink delivers a published meeting into the canonical
// recordings tree in Nextcloud Files, which is where D-521 puts the archive:
// one owner, per-file ACLs, and the operator proxying reads as the caller so
// Nextcloud itself enforces access.
//
// It is STRICT. A meeting that did not reach Nextcloud is not a published
// meeting, so every step returns an error and the publish fails. Before this,
// delivery was best-effort and ran *after* the publish was marked succeeded, so
// a job could report success with the recording nowhere in Nextcloud and the
// viewer 404ing (D-549).
//
//	attempt site (ONE meeting)
//	   │
//	   ├─ 1. precondition: the .opus this job published is on disk
//	   ├─ 2. MKCOL Cassini{,/Recordings{,/meetings}}         idempotent
//	   ├─ 3. PUT  meetings/<jobID>.opus        ← the object, FIRST
//	   │        + create-time deny so a new leaf cannot inherit the
//	   │          container's broad traversal grant before it is ACL'd
//	   ├─ 4. GET  catalog.json  → upsert this meeting → PUT      ← index, LAST
//	   └─ 5. per-file ACL: the meeting's audience
//
// Step 4 reads-merges-writes rather than uploading the local catalog, and that
// is load-bearing: the attempt site's catalog names exactly one meeting, so
// PUTting it verbatim would truncate the remote archive to that meeting. It
// also means the remote catalog is only ever added to — a failed delivery can
// never blank or narrow it.
const publishSinkNextcloudFiles = "nextcloud-files"

type nextcloudFilesPublishSink struct {
	cfg    ExAppConfig
	logger *log.Logger
	client *http.Client
	// applyAccess freezes the meeting's audience. It needs the Talk binding and
	// participant fetcher, which live on the Runtime, so it is injected rather
	// than reached for.
	applyAccess func(ctx context.Context, jobID string) error
}

func (s *nextcloudFilesPublishSink) Name() string { return publishSinkNextcloudFiles }

func (s *nextcloudFilesPublishSink) Deliver(ctx context.Context, d publishDelivery) (string, error) {
	incoming, ok, err := loadSiteCatalog(d.AttemptSitePath)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("attempt site %s has no catalog.json", d.AttemptSitePath)
	}
	if len(incoming.Meetings) == 0 {
		return "", fmt.Errorf("attempt site %s published no meetings", d.AttemptSitePath)
	}

	// Every asset the incoming catalog names must exist locally before anything
	// is uploaded. Without this the loop below could complete having delivered
	// nothing and still report success — reintroducing the silent hole this
	// sink exists to close.
	type upload struct{ local, remote string }
	var uploads []upload
	for _, entry := range incoming.Meetings {
		assets, err := catalogEntryAssets(entry)
		if err != nil {
			return "", err
		}
		for _, asset := range assets {
			local := filepath.Join(d.AttemptSitePath, asset)
			if _, err := os.Stat(local); err != nil {
				return "", fmt.Errorf("attempt site is missing catalog asset %s: %w", asset, err)
			}
			uploads = append(uploads, upload{local: local, remote: ncRecordingsRoot + "/" + filepath.ToSlash(asset)})
		}
	}
	if len(uploads) == 0 {
		return "", fmt.Errorf("attempt site %s names no deliverable assets", d.AttemptSitePath)
	}

	for _, dir := range []string{
		path.Dir(ncRecordingsRoot),     // Cassini
		ncRecordingsRoot,               // Cassini/Recordings
		ncRecordingsRoot + "/meetings", // Cassini/Recordings/meetings
	} {
		if dir == "." || dir == "" {
			continue
		}
		if err := s.cfg.davMkcol(ctx, s.client, ncRecordingsOwner, dir); err != nil {
			return "", fmt.Errorf("mkcol %s: %w", dir, err)
		}
	}

	for _, item := range uploads {
		status, err := s.cfg.davPutFileStatus(ctx, s.client, ncRecordingsOwner, item.remote, item.local, "audio/ogg")
		if err != nil {
			return "", fmt.Errorf("put %s: %w", item.remote, err)
		}
		if status == http.StatusCreated {
			// A newly created leaf inherits the container's traversal grant to
			// the virtual `everyone` group. Deny it before the catalog can
			// advertise the file; the per-meeting ACL below replaces this
			// owner-only baseline.
			if err := s.cfg.davProppatchACLRules(ctx, s.client, ncRecordingsOwner, item.remote, recordingACLRules(nil, false)); err != nil {
				return "", fmt.Errorf("protect new %s: %w", item.remote, err)
			}
		}
	}

	if err := s.upsertRemoteCatalog(ctx, incoming); err != nil {
		return "", err
	}

	if s.applyAccess != nil {
		if err := s.applyAccess(ctx, d.JobID); err != nil {
			return "", fmt.Errorf("access: %w", err)
		}
	}
	return ncRecordingsRoot, nil
}

// upsertRemoteCatalog merges the delivered meetings into the catalog already in
// Nextcloud Files and writes it back. Read-merge-write, never overwrite: the
// attempt site knows about one meeting and the archive knows about all of them.
func (s *nextcloudFilesPublishSink) upsertRemoteCatalog(ctx context.Context, incoming siteCatalog) error {
	catalogRemote := ncRecordingsRoot + "/catalog.json"

	var existing siteCatalog
	raw, status, err := s.cfg.davGetBytes(ctx, s.client, ncRecordingsOwner, catalogRemote)
	switch {
	case err != nil && status != http.StatusNotFound:
		return fmt.Errorf("read remote catalog: %w", err)
	case status == http.StatusNotFound:
		// First delivery into an empty archive.
	default:
		if err := json.Unmarshal(raw, &existing); err != nil {
			// Overwriting an unreadable catalog would silently drop the archive
			// it indexes. Refuse and leave it for a human.
			return fmt.Errorf("parse remote catalog: %w", err)
		}
	}

	merged, err := upsertSiteCatalog(existing, incoming)
	if err != nil {
		return err
	}
	if merged.Meetings == nil {
		merged.Meetings = []json.RawMessage{}
	}
	body, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal remote catalog: %w", err)
	}
	body = append(body, '\n')
	if err := s.cfg.davPutBytes(ctx, s.client, ncRecordingsOwner, catalogRemote, body, "application/json"); err != nil {
		return fmt.Errorf("put catalog: %w", err)
	}
	// The virtual all-users group may traverse the container directories but must
	// not read the unfiltered authoritative catalog; the operator reads it
	// as the owner and filters it per caller.
	if err := s.cfg.davProppatchACLRules(ctx, s.client, ncRecordingsOwner, catalogRemote, catalogProtectionACLRules()); err != nil {
		return fmt.Errorf("protect catalog: %w", err)
	}
	return nil
}

// davPutBytes uploads an in-memory body, for artefacts the operator composes
// rather than copies (the merged catalog).
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

// defaultPublishSinkFor resolves an unset selection from the deployment shape.
//
// An ExApp gets nextcloud-files. This is the fail-safe half of the design: a
// production install whose CASSINI_PUBLISH_SINK never arrives — AppAPI only
// injects variables appinfo/info.xml declares, and a dropped declaration is a
// silent failure mode this codebase has already been bitten by (D-403) — must
// not quietly fall back to keeping recordings on the app's own volume.
//
// APP_SECRET is injected by AppAPI itself rather than declared in the manifest,
// so unlike a declared variable it cannot be dropped. That makes it a sound
// signal for "this is an ExApp" even though it is not a sound signal for
// "Nextcloud is reachable" — which is why the name, once resolved, is still an
// explicit declaration the operator reports and acts on rather than re-derives.
func defaultPublishSinkFor(exapp ExAppConfig) string {
	if exapp.Active {
		return publishSinkNextcloudFiles
	}
	return publishSinkLocal
}

// newPublishSinkFor builds the named sink, including the ones that need more
// than Config. Unknown names are rejected here as well as in loadConfig, since
// a resolved default has not been through that validation.
func newPublishSinkFor(name string, cfg Config, exapp ExAppConfig, rt *Runtime, logger *log.Logger) (publishSink, error) {
	switch name {
	case publishSinkNextcloudFiles:
		if !exapp.appAPIActive() {
			return nil, fmt.Errorf(
				"sink %q needs NEXTCLOUD_URL, APP_ID and APP_SECRET; set CASSINI_PUBLISH_SINK=%s to keep recordings on this machine instead",
				publishSinkNextcloudFiles, publishSinkLocal)
		}
		return &nextcloudFilesPublishSink{
			cfg:         exapp,
			logger:      logger,
			client:      &http.Client{Timeout: ncFilesUploadTimeout},
			applyAccess: rt.applyNCFilesAccessStrict,
		}, nil
	default:
		return newPublishSink(name, cfg, logger)
	}
}
