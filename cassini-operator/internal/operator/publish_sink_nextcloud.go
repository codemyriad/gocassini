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
//	   ├─ 3. per asset, deliverAsset:
//	   │        PROPFIND        ← health gate: is it there, and is it ruled?
//	   │        PUT (empty)     ← the leaf exists before any audio does
//	   │        PROPPATCH deny  ← owner-only, unconditional
//	   │        PUT (bytes)     ← fileid unchanged, so the deny still covers it
//	   │        PROPFIND        ← post-condition: Nextcloud stored what we sent
//	   ├─ 4. per-file ACL: the meeting's audience                ← access, THEN
//	   └─ 5. GET catalog.json → upsert this meeting → PUT        ← index, LAST
//
// Steps 3-5 are ordered the way they are because of D-594: the object must never
// be reachable with content in it and no rules on it, and it must never be
// advertised before its audience is frozen. The bytes therefore land last within
// a leaf, and the catalog last overall.
//
// Step 5 reads-merges-writes rather than uploading the local catalog, and that
// is load-bearing: the attempt site's catalog names exactly one meeting, so
// PUTting it verbatim would truncate the remote archive to that meeting. It
// also means the remote catalog is only ever added to — a failed delivery can
// never blank or narrow it.
const publishSinkNextcloudFiles = "nextcloud-files"

// ncRecordingsContentType is what a recording is uploaded as. Nextcloud maps
// `.opus` to audio/ogg for playback either way; sending it explicitly keeps the
// empty reservation and the content PUT identical in everything but length.
const ncRecordingsContentType = "audio/ogg"

// envPublishSinkName is the deploy option that selects the sink. Named here
// because the strict substrate gate below points an operator at it.
const envPublishSinkName = "CASSINI_PUBLISH_SINK"

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
	// Refuse to write into a directory that is not the Team folder (D-585
	// outcome 5, decided strict).
	//
	// WebDAV cannot tell us this. With no group folder mounted at `Cassini`,
	// MKCOL of Cassini/Recordings creates an ordinary directory in the service
	// account's own HOME and returns the same 201 as a mounted group folder
	// would; the .opus PUTs succeed; the publish reports success; and every
	// caller's PROPFIND of their own tree 404s forever. Every individual call
	// succeeds and the composition is wrong, so the only sound guard is what
	// provisioning recorded.
	//
	// Strict rather than a degraded mode, consistent with D-549/D-550: a
	// recording that did not reach Nextcloud is not published. An operator who
	// genuinely wants recordings on the app's own volume already has
	// CASSINI_PUBLISH_SINK=local, which is a different sink object entirely and
	// never reaches this code.
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); snap.Applicable && !ncAccessSubstrate.usable() {
		return "", fmt.Errorf("the recordings substrate is not provisioned (%s: %s); refusing to write recordings into the %q account's private home — see GET /status recordings_access, or set %s=local to keep recordings on this app's own volume",
			snap.Step, snap.Detail, ncRecordingsOwner, envPublishSinkName)
	}
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

	// Every asset the incoming catalog names must exist locally, and match the
	// digest its job sealed, before anything is uploaded. Without this the loop
	// below could complete having delivered nothing and still report success —
	// reintroducing the silent hole this sink exists to close.
	var uploads []upload
	for _, entry := range incoming.Meetings {
		assets, err := catalogEntryAssets(entry)
		if err != nil {
			return "", err
		}
		for _, asset := range assets {
			local := filepath.Join(d.AttemptSitePath, asset)
			info, err := os.Stat(local)
			if err != nil {
				return "", fmt.Errorf("attempt site is missing catalog asset %s: %w", asset, err)
			}
			// The last link in the seal chain (D-583). The local sink checks this
			// in stageAsset; this sink did not, so on an installed ExApp — the
			// only deployment that uses it — the chain was two links long instead
			// of three. Checked here rather than per-PUT so a mismatch costs no
			// upload at all, matching the existing "verify everything first"
			// preflight above.
			//
			// Only file assets are digested: a directory asset is a legacy
			// artifactPath export with no single artifact to have sealed. Like
			// stageAsset this proves the bytes leaving here are the sealed ones;
			// it does not attest what the server stored, which WebDAV gives us no
			// portable way to read back.
			if want, ok := d.AssetDigests[filepath.ToSlash(asset)]; ok && !info.IsDir() {
				got, err := fileSHA256(local)
				if err != nil {
					return "", err
				}
				if got != want {
					return "", fmt.Errorf("refusing to publish %s: it does not match the artifact this job sealed (sha256 %s, want %s)", asset, got, want)
				}
			}
			uploads = append(uploads, upload{
				local:  local,
				remote: ncRecordingsRoot + "/" + filepath.ToSlash(asset),
				size:   info.Size(),
				isDir:  info.IsDir(),
			})
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

	// The audience is written once, onto the meeting's .opus, and only when that
	// leaf has not already got one. Which upload that is has to be decided here,
	// against the same path ncFilesAccessApplier will target, so a layout that
	// drifts is caught by the tests rather than by a silently unprotected file.
	opusRemote := ncRecordingsRoot + "/meetings/" + d.JobID + ".opus"
	audienceNeeded := false

	for _, item := range uploads {
		fresh, err := s.deliverAsset(ctx, item)
		if err != nil {
			return "", err
		}
		if item.remote == opusRemote && fresh {
			audienceNeeded = true
		}
	}

	// Freezing the audience comes BEFORE the catalog, so a meeting whose ACL did
	// not land is never advertised — the catalog is the thing that makes a
	// recording discoverable, and writing it first is what let a failed ACL
	// produce an indexed, readable recording.
	if audienceNeeded && s.applyAccess != nil {
		if err := s.applyAccess(ctx, d.JobID); err != nil {
			return "", fmt.Errorf("access: %w", err)
		}
	}

	if err := s.upsertRemoteCatalog(ctx, incoming); err != nil {
		return "", err
	}
	return ncRecordingsRoot, nil
}

// upload is one asset on its way to the archive, with what the post-condition
// needs to judge whether it arrived intact.
type upload struct {
	local, remote string
	size          int64
	// isDir marks a legacy artifactPath export, which has no single file whose
	// length can be compared.
	isDir bool
}

// deliverAsset puts one asset into the archive so that it is never reachable
// with content in it and no rules on it, and reports whether the leaf still
// needs its audience written.
//
// The ordering is the whole fix (D-594). A leaf that states no rules of its own
// inherits the container's `everyone: READ`, so the bytes must not exist until a
// rule set does. Since there is no atomic create-with-ACL for a file, the leaf is
// created EMPTY, denied, and only then filled — an overwriting PUT keeps the
// file's fileid, and groupfolders keys its rules by fileid, so the deny written
// against the empty file still covers the audio.
//
//	absent                  -> PUT empty, deny, PUT bytes        (+ audience)
//	present, no everyone row-> deny, DELETE, then as absent       (+ audience)
//	present, no audience yet-> PUT bytes                          (+ audience)
//	present, audience set   -> PUT bytes, ACL untouched
//
// The last row is deliberate: a re-delivery replaces content, never access. An
// audience someone widened by hand in the Files UI survives it.
func (s *nextcloudFilesPublishSink) deliverAsset(ctx context.Context, item upload) (audienceNeeded bool, err error) {
	if false {
		// A legacy artifactPath export is a directory, which has no single leaf
		// to reserve, no length to verify, and — the reason for the early exit
		// rather than a few skipped steps — nothing that should ever be fed to
		// the repair branch, where a missing rule set would DELETE the tree.
		// This asset shape is carried exactly as it was before D-594.
		return false, s.putAssetBytes(ctx, item)
	}

	state, err := s.cfg.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, item.remote)
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", item.remote, err)
	}

	switch {
	case state.Exists && !hasExplicitEveryoneGroupRule(state.Rules):
		// The D-594 state itself: a delivered recording carrying no broad-group
		// rule, readable by every account. Repair it before replacing it —
		// deleting it first would only move the exposure into a trash every
		// account can read.
		if err := s.repairUnprotectedLeaf(ctx, item.remote); err != nil {
			return false, err
		}
		if err := s.createProtectedLeaf(ctx, item); err != nil {
			return false, err
		}
		audienceNeeded = true
	case !state.Exists:
		if err := s.createProtectedLeaf(ctx, item); err != nil {
			return false, err
		}
		audienceNeeded = true
	default:
		// Already protected. Whether the audience still has to be written is the
		// difference between "the create-time deny is all that ever landed" and
		// "this meeting's audience is frozen" — see audienceApplied.
		audienceNeeded = !audienceApplied(state.Rules)
	}

	if err := s.putAssetBytes(ctx, item); err != nil {
		return false, err
	}
	return audienceNeeded, nil
}

// createProtectedLeaf establishes the leaf with an owner-only ACL and no content.
func (s *nextcloudFilesPublishSink) createProtectedLeaf(ctx context.Context, item upload) error {
	if _, err := s.cfg.davPutEmpty(ctx, s.client, ncRecordingsOwner, item.remote, ncRecordingsContentType); err != nil {
		return fmt.Errorf("reserve %s: %w", item.remote, err)
	}
	// Unconditional, never gated on the PUT having returned 201. Gating it is
	// what let a re-delivery — which answers 204 — skip the deny entirely and
	// leave a previously unprotected recording unprotected.
	if err := s.cfg.davProppatchACLRules(ctx, s.client, ncRecordingsOwner, item.remote, recordingACLRules(nil, false)); err != nil {
		return fmt.Errorf("protect new %s: %w", item.remote, err)
	}
	return nil
}

// repairUnprotectedLeaf denies the broad group on a leaf that has no rules and
// then removes it.
//
// The deny is not belt-and-braces: a group-folder DELETE moves the bytes to the
// Team-folder trash, and the trash gate consults the leaf's own rules, so
// deleting an unruled recording leaves it listable and downloadable by every
// account from their own trashbin. Denying first is what makes the trash copy
// inherit the protection.
func (s *nextcloudFilesPublishSink) repairUnprotectedLeaf(ctx context.Context, remote string) error {
	if err := s.cfg.davProppatchACLRules(ctx, s.client, ncRecordingsOwner, remote, recordingACLRules(nil, false)); err != nil {
		return fmt.Errorf("protect unprotected %s before removing it: %w", remote, err)
	}
	if s.logger != nil {
		s.logger.Printf("nc files: %s was delivered without an access rule — denied and replaced", remote)
	}
	if err := s.cfg.davDelete(ctx, s.client, ncRecordingsOwner, remote); err != nil {
		return fmt.Errorf("remove unprotected %s: %w", remote, err)
	}
	return nil
}

// putAssetBytes uploads the content and verifies what Nextcloud stored.
//
// The read-back is the only thing standing between an interrupted upload and a
// silently truncated recording: Nextcloud commits the short bytes to the
// published path, keeps the fileid and keeps the ACL, so nothing downstream can
// tell. Comparing the stored length against what we sent turns that into a failed
// publish the operator can re-run.
func (s *nextcloudFilesPublishSink) putAssetBytes(ctx context.Context, item upload) error {
	if _, err := s.cfg.davPutFileStatus(ctx, s.client, ncRecordingsOwner, item.remote, item.local, ncRecordingsContentType); err != nil {
		return fmt.Errorf("put %s: %w", item.remote, err)
	}
	if item.isDir {
		return nil
	}
	state, err := s.cfg.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, item.remote)
	if err != nil {
		return fmt.Errorf("verify %s: %w", item.remote, err)
	}
	if !state.Exists {
		return fmt.Errorf("verify %s: it is not there after a successful upload", item.remote)
	}
	if state.Size != item.size {
		return fmt.Errorf("refusing to publish %s: Nextcloud stored %d bytes of %d — the upload was interrupted and the remote copy is truncated; re-run the publish",
			item.remote, state.Size, item.size)
	}
	return nil
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
