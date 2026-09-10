package operator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The nextcloud-files sink delivers a published meeting into the canonical
// recordings tree in Nextcloud Files (D-521): one owner, per-file ACLs, and the
// operator proxying reads as the caller so Nextcloud itself enforces access.
//
// It is STRICT: a meeting that did not reach Nextcloud is not published, so
// every step returns an error and the publish fails (D-549).
//
//	attempt site (ONE meeting)
//	   │
//	   ├─ 1. precondition: the .opus this job published is on disk
//	   ├─ 2. MKCOL Cassini{,/Recordings{,/meetings}}         idempotent
//	   ├─ 3. GET catalog.json   ← read only; is this meeting already indexed?
//	   ├─ 4. per asset, deliverAsset:
//	   │        PROPFIND        ← health gate: is it there, and is it ruled?
//	   │        PUT (empty)     ← the leaf exists before any audio does
//	   │        PROPPATCH deny  ← owner-only, unconditional
//	   │        PUT (bytes)     ← fileid unchanged, so the deny still covers it
//	   │        PROPFIND        ← post-condition: stored length AND rules
//	   │      a .opus that is ALREADY there is a re-delivery, and its bytes are
//	   │      the sealed audio plus the marks on the delivered copy (D-737):
//	   │        GET             ← the delivered copy, at the gate's ETag
//	   │        annotate carry  ← its marks into a staged copy of the sealed file
//	   │        PUT If-Match    ← 412: a mark landed meanwhile; read it again
//	   ├─ 5. per-file ACL: the meeting's audience                ← access, THEN
//	   ├─ 6. upsert this meeting into catalog.json → PUT         ← index, LAST
//	   └─ 7. record the delivered marks in the projection   ← never fails a publish
//
// Steps 4-6 are ordered for D-594: the object must never be reachable with
// content in it and no rules on it, and never advertised before its audience is
// frozen. So the bytes land last within a leaf, and the catalog last overall.
//
// Step 3 comes first because the index is the only durable evidence of whether
// a meeting's audience was ever written, which step 4 needs to tell an
// unfinished publish from an administrator's deliberate narrowing.
//
// Step 6 reads-merges-writes: the attempt site's catalog names one meeting, so
// PUTting it verbatim would truncate the remote archive to that meeting.
const publishSinkNextcloudFiles = "nextcloud-files"

// ncRecordingsContentType keeps the empty reservation and the content PUT
// identical in everything but length.
const ncRecordingsContentType = "audio/ogg"

const envPublishSinkName = "CASSINI_PUBLISH_SINK"

type nextcloudFilesPublishSink struct {
	cfg    ExAppConfig
	logger *log.Logger
	client *http.Client
	// applyAccess freezes the meeting's audience; it needs the Talk binding,
	// which lives on the Runtime.
	applyAccess func(ctx context.Context, jobID string) error
	// cassiniBin carries a delivered copy's marks forward (D-737). Empty means
	// none can exist: the routes that write marks are not mounted without it.
	cassiniBin string
	// rt holds the marks projection, read at delivery time so the order the
	// Runtime is assembled in does not matter. Nil: no projection.
	rt *Runtime
}

func (s *nextcloudFilesPublishSink) Name() string { return publishSinkNextcloudFiles }

func (s *nextcloudFilesPublishSink) Deliver(ctx context.Context, d publishDelivery) (string, error) {
	// Refuse to write into a directory that is not the Team folder (D-585).
	// WebDAV cannot tell: with no group folder mounted at `Cassini`, MKCOL creates
	// an ordinary directory in the service account's HOME and every call
	// succeeds, so only what provisioning recorded is a sound guard.
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); snap.Applicable && !ncAccessSubstrate.usable() {
		return "", fmt.Errorf("the recordings storage is not ready (%s: %s); refusing to publish — see GET /status recordings_access and the Setup tab in the Cassini app, or set %s=local to keep recordings on this app's own volume",
			snap.Step, snap.Detail, envPublishSinkName)
	}
	// A delivery and a storage-mode switch may not overlap: the switch's last
	// step empties the old root, and a delivery landing there in between would
	// be deleted after its job was marked succeeded. Nothing below re-enters it.
	provisionMu.Lock()
	defer provisionMu.Unlock()

	// Read once, under the lock, so one delivery cannot put assets in both trees
	// or leave one ruled and the next not (D-616).
	accessControlled := ncStorage.accessControlled()
	root := recordingsRootFor(accessControlled)
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

	// Every asset must exist locally, and match the digest its job sealed
	// (D-583), before anything is uploaded — otherwise the loop below could
	// deliver nothing and still report success. A directory asset is a legacy
	// artifactPath export with no single sealed artifact.
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
				remote: root + "/" + filepath.ToSlash(asset),
				size:   info.Size(),
				isDir:  info.IsDir(),
			})
		}
	}
	if len(uploads) == 0 {
		return "", fmt.Errorf("attempt site %s names no deliverable assets", d.AttemptSitePath)
	}

	for _, dir := range recordingsTreeDirs(root) {
		if dir == "." || dir == "" {
			continue
		}
		if err := s.cfg.davMkcol(ctx, s.client, ncRecordingsOwner, dir); err != nil {
			return "", fmt.Errorf("mkcol %s: %w", dir, err)
		}
	}

	// The audience is written once, onto the meeting's .opus, the path
	// ncFilesAccessApplier targets. attemptOpusPath hardcodes <jobID>.opus; if
	// that drifts this fails closed, because createProtectedLeaf still denies.
	opusRemote := root + "/meetings/" + d.JobID + ".opus"
	audienceNeeded := false

	// A leaf at exactly the owner-only baseline is ambiguous: an unfinished
	// publish leaves it, and so does an administrator narrowing a recording in
	// the Files UI. The catalog is written last, so a meeting it names got past
	// the audience step, and re-deriving the audience would revert that decision.
	existingCatalog, catalogMissing, err := s.readRemoteCatalog(ctx, root)
	if err != nil {
		return "", err
	}
	alreadyIndexed := catalogNamesMeeting(existingCatalog, d.JobID)

	carried := map[string]annotateResult{}
	for _, item := range uploads {
		fresh, marks, err := s.deliverAsset(ctx, item, alreadyIndexed, accessControlled)
		if err != nil {
			return "", err
		}
		if marks != nil {
			carried[item.remote] = *marks
		}
		if item.remote == opusRemote && fresh {
			audienceNeeded = true
		}
	}

	// The audience before the catalog, so a meeting whose ACL did not land is
	// never advertised. The default model has none: the tree is the service
	// account's own, and nc:acl-list is only settable in a Team folder with
	// advanced ACL (elsewhere it answers 207 with a 403 propstat).
	if accessControlled && audienceNeeded && s.applyAccess != nil {
		if err := s.applyAccess(ctx, d.JobID); err != nil {
			return "", fmt.Errorf("access: %w", err)
		}
	}

	if err := s.upsertRemoteCatalog(ctx, existingCatalog, catalogMissing, incoming, catalogEntryOverlay{RoomName: d.RoomName}, accessControlled, root); err != nil {
		return "", err
	}
	s.indexDeliveredMarks(ctx, uploads, carried)
	return root, nil
}

func catalogNamesMeeting(catalog siteCatalog, jobID string) bool {
	for _, entry := range catalog.Meetings {
		id, err := catalogEntryID(entry)
		if err != nil {
			continue
		}
		if id == jobID {
			return true
		}
	}
	return false
}

// upload is one asset on its way to the archive.
type upload struct {
	local, remote string
	size          int64
	// isDir marks a legacy artifactPath export, which has no single file whose
	// length can be compared.
	isDir bool
}

// deliverAsset puts one asset into the archive so that it is never reachable
// with content in it and no rules on it (D-594). It reports whether the leaf
// still needs its audience written, and carry's report where marks were carried.
//
// A leaf with no rules of its own inherits the container's `everyone: READ`, and
// there is no atomic create-with-ACL, so the leaf is created EMPTY, denied, and
// then filled: an overwriting PUT keeps the fileid, which groupfolders keys its
// rules by.
//
//	absent                  -> PUT empty, deny, PUT bytes        (+ audience)
//	present, no everyone row-> deny, DELETE, then as absent       (+ audience)
//	present, no audience yet-> PUT bytes                          (+ audience)
//	present, audience set   -> PUT bytes, ACL untouched
//
// A re-delivery replaces content, never access, so an audience widened by hand
// survives it. Where the leaf is present, "bytes" is the sealed audio plus the
// marks on the delivered copy (D-737).
//
// The default model (D-616) skips all of it: the tree is private to the service
// account, and the PROPPATCH would be rejected outside a Team folder.
func (s *nextcloudFilesPublishSink) deliverAsset(ctx context.Context, item upload, alreadyIndexed, accessControlled bool) (audienceNeeded bool, marks *annotateResult, err error) {
	if !accessControlled {
		if !s.carriesMarks(item) {
			return false, nil, s.putAssetBytes(ctx, item, accessControlled, "")
		}
		// No health gate in this model, so ask here whether there is a delivered
		// copy to carry from.
		state, err := s.cfg.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, item.remote)
		if err != nil {
			return false, nil, fmt.Errorf("inspect %s: %w", item.remote, err)
		}
		if !state.Exists {
			return false, nil, s.putAssetBytes(ctx, item, accessControlled, "")
		}
		marks, err := s.putOverDeliveredCopy(ctx, item, state, accessControlled)
		return false, marks, err
	}
	if item.isDir {
		// No leaf to reserve, and it must never reach the repair branch, whose
		// DELETE would remove the tree.
		return false, nil, s.putAssetBytes(ctx, item, accessControlled, "")
	}

	state, err := s.cfg.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, item.remote)
	if err != nil {
		return false, nil, fmt.Errorf("inspect %s: %w", item.remote, err)
	}

	content := item
	switch {
	case state.Exists && !everyoneRuleGovernsRead(state.Rules):
		// Readable by every account. Its marks are carried out first, because the
		// repair deletes the leaf and every mark on it; the write endpoint refuses
		// an unruled leaf, so none can land meanwhile.
		if s.carriesMarks(item) {
			dir, cleanup, err := newCarryDir(item)
			if err == nil {
				defer cleanup()
				var result annotateResult
				if content, result, err = s.stageDeliveredMarks(ctx, item, dir); err == nil {
					marks = &result
				}
			}
			if err != nil {
				// A carry that keeps refusing (marks a rolled-back build cannot read)
				// must not leave the recording readable by every account on every
				// rerun: deny it, keep its marks, and fail.
				if denyErr := s.cfg.davProppatchACLRules(ctx, s.client, ncRecordingsOwner, item.remote, recordingACLRules(nil, false)); denyErr != nil {
					return false, nil, errors.Join(err, fmt.Errorf("protect unprotected %s: %w", item.remote, denyErr))
				}
				s.logf("nc files: %s was delivered without an access rule — denied; its marks could not be carried, so it was not replaced", item.remote)
				return false, nil, err
			}
		}
		if err := s.repairUnprotectedLeaf(ctx, item.remote); err != nil {
			return false, nil, err
		}
		if err := s.createProtectedLeaf(ctx, item); err != nil {
			return false, nil, err
		}
		audienceNeeded = true
	case !state.Exists:
		if err := s.createProtectedLeaf(ctx, item); err != nil {
			return false, nil, err
		}
		audienceNeeded = true
	default:
		// alreadyIndexed breaks the tie at the owner-only baseline (see Deliver).
		audienceNeeded = !audienceApplied(state.Rules) && !alreadyIndexed
		if s.carriesMarks(item) {
			marks, err := s.putOverDeliveredCopy(ctx, item, state, accessControlled)
			if err != nil {
				return false, nil, err
			}
			return audienceNeeded, marks, nil
		}
	}

	if err := s.putAssetBytes(ctx, content, accessControlled, ""); err != nil {
		return false, nil, err
	}
	return audienceNeeded, marks, nil
}

// maxCarryAttempts: a person marking by hand does not commit three times inside
// one fetch-carry-PUT, so a third refusal means something writes continuously.
const maxCarryAttempts = 3

// putOverDeliveredCopy re-delivers item over a recording already in the archive,
// carrying the marks written on it since. Marks live only on the delivered
// copy, so the sealed file alone would erase them. If-Match keeps a mark
// committed between the GET and the PUT from being overwritten by a staged copy
// that never saw it.
func (s *nextcloudFilesPublishSink) putOverDeliveredCopy(ctx context.Context, item upload, state ncLeafState, accessControlled bool) (*annotateResult, error) {
	dir, cleanup, err := newCarryDir(item)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			if state, err = s.cfg.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, item.remote); err != nil {
				return nil, fmt.Errorf("inspect %s: %w", item.remote, err)
			}
			if !state.Exists {
				// Re-deriving its access from here would be guessing; the next
				// publish takes the absent branch.
				return nil, fmt.Errorf("refusing to publish %s: it disappeared while this rerun was carrying its marks forward; re-run the publish", item.remote)
			}
		}
		if state.ETag == "" {
			return nil, fmt.Errorf("refusing to publish %s: Nextcloud reported no ETag for it, so it cannot be replaced without risking a mark written meanwhile; re-run the publish", item.remote)
		}
		staged, result, err := s.stageDeliveredMarks(ctx, item, dir)
		if err != nil {
			return nil, err
		}
		err = s.putAssetBytes(ctx, staged, accessControlled, state.ETag)
		if err == nil {
			return &result, nil
		}
		if !errors.Is(err, errDAVPreconditionFailed) {
			return nil, err
		}
		if attempt >= maxCarryAttempts {
			return nil, fmt.Errorf("refusing to publish %s: it changed under this rerun %d times — marks are being written to it right now; re-run the publish: %w",
				item.remote, attempt, err)
		}
		s.logf("nc files: %s changed while its marks were being carried (attempt %d of %d) — reading it again", item.remote, attempt, maxCarryAttempts)
	}
}

// stageDeliveredMarks fetches the delivered copy of item and has carry write its
// marks into a staged copy of the sealed file, which it answers with carry's
// report. The sealed file is never modified, so the seal preflight still proves
// the audio, and carry's own verification proves the marks
// (spec/cassini-opus-audio-integrity-v1.md, "Three digests").
func (s *nextcloudFilesPublishSink) stageDeliveredMarks(ctx context.Context, item upload, dir string) (upload, annotateResult, error) {
	delivered := filepath.Join(dir, "delivered.opus")
	staged := filepath.Join(dir, "staged.opus")
	// A retry reuses the directory; nothing read at a stale ETag may leak into it.
	for _, p := range []string{delivered, staged} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return upload{}, annotateResult{}, err
		}
	}
	if _, _, _, err := s.cfg.davDownloadFile(ctx, s.client, ncRecordingsOwner, item.remote, delivered, 0); err != nil {
		return upload{}, annotateResult{}, fmt.Errorf("fetch the delivered %s to carry its marks: %w", item.remote, err)
	}
	result, err := runAnnotateCarry(ctx, s.cassiniBin, delivered, item.local, staged)
	if err != nil {
		return s.sealedIfDeliveredIsUnreadable(ctx, item, delivered, err)
	}
	info, err := os.Stat(staged)
	if err != nil {
		return upload{}, annotateResult{}, fmt.Errorf("carry the marks on %s: it reported success and wrote nothing: %w", item.remote, err)
	}
	if result.Resolved != nil && !*result.Resolved {
		// Silvio's rule: marks made against different audio are kept and flagged,
		// never dropped. A delivery, not a failure; logged for whoever asks why.
		s.logf("nc files: %s: %d mark(s) on the delivered copy were made against different audio — carried unresolved and delivered", item.remote, result.Carried)
	}
	return upload{local: staged, remote: item.remote, size: info.Size()}, result, nil
}

// sealedIfDeliveredIsUnreadable decides what a failed carry means.
//
// Exactly one failure is safe to deliver past: the delivered copy is not a
// recording at all — the zero-byte reservation of a first publish that died, or
// a truncated upload. No mark can have been committed into either, and refusing
// would wedge the meeting forever. The CLI reads each file on its own to tell:
// the delivered copy must be unreadable AND the sealed one readable. Anything
// else fails the publish, because delivering past it loses every mark.
func (s *nextcloudFilesPublishSink) sealedIfDeliveredIsUnreadable(ctx context.Context, item upload, delivered string, carryErr error) (upload, annotateResult, error) {
	if _, err := runAnnotateShow(ctx, s.cassiniBin, delivered); err == nil {
		return upload{}, annotateResult{}, fmt.Errorf("carry the marks on %s into this rerun: %w", item.remote, carryErr)
	}
	sealed, err := runAnnotateShow(ctx, s.cassiniBin, item.local)
	if err != nil {
		return upload{}, annotateResult{}, fmt.Errorf("carry the marks on %s into this rerun: %w (and the CLI cannot read the sealed file either: %v)", item.remote, carryErr, err)
	}
	s.logf("nc files: the delivered %s is not a readable recording, so it carries no marks — delivering the sealed file (%v)", item.remote, carryErr)
	return item, sealed, nil
}

// carriesMarks: only a recording carries marks, and only the CLI can move them.
func (s *nextcloudFilesPublishSink) carriesMarks(item upload) bool {
	return !item.isDir && strings.HasSuffix(item.remote, ".opus") && strings.TrimSpace(s.cassiniBin) != ""
}

// newCarryDir is beside the sealed file rather than in /tmp: on the operator's
// volume (a tmpfs /tmp would hold two recordings in memory), and inside the
// attempt site, which a successful publish removes (D-550).
func newCarryDir(item upload) (string, func(), error) {
	dir, err := os.MkdirTemp(filepath.Dir(item.local), ".carry-")
	if err != nil {
		return "", nil, fmt.Errorf("carry scratch for %s: %w", item.remote, err)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

const publishAnnotationsUnreadable = "marks-unreadable"

// indexDeliveredMarks records what each delivered recording now carries in the
// marks projection, once the delivery is complete. A first publish delivered
// the sealed file, which is read where it lies. Nothing here fails a publish: a
// recording that could not be recorded is marked unavailable instead.
func (s *nextcloudFilesPublishSink) indexDeliveredMarks(ctx context.Context, uploads []upload, carried map[string]annotateResult) {
	if s.rt == nil || s.rt.annotations == nil {
		return
	}
	index := s.rt.annotations
	for _, item := range uploads {
		if !s.carriesMarks(item) {
			continue
		}
		name := path.Base(item.remote)
		result, ok := carried[item.remote]
		if !ok {
			var err error
			if result, err = runAnnotateShow(ctx, s.cassiniBin, item.local); err != nil {
				s.markAnnotationsUnavailable(ctx, index, name, fmt.Errorf("read marks: %w", err))
				continue
			}
		}
		if err := index.Record(ctx, name, result); err != nil {
			s.markAnnotationsUnavailable(ctx, index, name, fmt.Errorf("record marks: %w", err))
		}
	}
}

func (s *nextcloudFilesPublishSink) markAnnotationsUnavailable(ctx context.Context, index annotationIndex, name string, cause error) {
	s.logf("annotations index: %s: %v — recorded as unavailable", name, cause)
	if err := index.MarkUnavailable(ctx, name, publishAnnotationsUnreadable); err != nil {
		s.logf("annotations index: %s: could not record it unavailable either: %v", name, err)
	}
}

func (s *nextcloudFilesPublishSink) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

// createProtectedLeaf establishes the leaf with an owner-only ACL and no content.
func (s *nextcloudFilesPublishSink) createProtectedLeaf(ctx context.Context, item upload) error {
	if _, err := s.cfg.davPutEmpty(ctx, s.client, ncRecordingsOwner, item.remote, ncRecordingsContentType); err != nil {
		return fmt.Errorf("reserve %s: %w", item.remote, err)
	}
	// Unconditional, never gated on a 201: a re-delivery answers 204, and gating
	// it left a previously unprotected recording unprotected.
	if err := s.cfg.davProppatchACLRules(ctx, s.client, ncRecordingsOwner, item.remote, recordingACLRules(nil, false)); err != nil {
		return fmt.Errorf("protect new %s: %w", item.remote, err)
	}
	return nil
}

// repairUnprotectedLeaf denies the broad group on a leaf that has no rules and
// then removes it. The deny comes first because a group-folder DELETE moves the
// bytes to the Team-folder trash, whose gate consults the leaf's own rules: an
// unruled recording deleted would stay downloadable by every account there.
func (s *nextcloudFilesPublishSink) repairUnprotectedLeaf(ctx context.Context, remote string) error {
	if err := s.cfg.davProppatchACLRules(ctx, s.client, ncRecordingsOwner, remote, recordingACLRules(nil, false)); err != nil {
		return fmt.Errorf("protect unprotected %s before removing it: %w", remote, err)
	}
	s.logf("nc files: %s was delivered without an access rule — denied and replaced", remote)
	if err := s.cfg.davDelete(ctx, s.client, ncRecordingsOwner, remote); err != nil {
		return fmt.Errorf("remove unprotected %s: %w", remote, err)
	}
	return nil
}

// putAssetBytes uploads the content, conditional on ifMatch when set, and
// verifies what Nextcloud stored. A 412 wraps errDAVPreconditionFailed and has
// written nothing.
func (s *nextcloudFilesPublishSink) putAssetBytes(ctx context.Context, item upload, accessControlled bool, ifMatch string) error {
	if _, _, err := s.cfg.davPutFileIfMatch(ctx, s.client, ncRecordingsOwner, item.remote, item.local, ncRecordingsContentType, ifMatch); err != nil {
		return fmt.Errorf("put %s: %w", item.remote, err)
	}
	if item.isDir {
		return nil
	}
	if err := s.cfg.verifyUploadedLeaf(ctx, s.client, item.remote, item.size, accessControlled); err != nil {
		return fmt.Errorf("refusing to publish: %w; re-run the publish", err)
	}
	return nil
}

// verifyUploadedLeaf is the post-condition of every upload of a recording.
//
// Nextcloud commits an interrupted upload as a truncated file with the same
// fileid and ACL, so only the stored length tells. And a leaf that lost its
// rules between the caller's PROPFIND and its PUT — deleted in the Files UI,
// restored from trash — became a new leaf with no rules and the audio in it.
// underACL is false in the default model, where a leaf has no rules by design.
func (c ExAppConfig) verifyUploadedLeaf(ctx context.Context, client *http.Client, relPath string, size int64, underACL bool) error {
	state, err := c.davPropfindLeafState(ctx, client, ncRecordingsOwner, relPath)
	switch {
	case err != nil:
		return fmt.Errorf("verify %s: %w", relPath, err)
	case !state.Exists:
		return fmt.Errorf("verify %s: it is not there after a successful upload", relPath)
	case state.Size != size:
		return fmt.Errorf("%s: Nextcloud stored %d bytes of %d — the upload was interrupted and the remote copy is truncated", relPath, state.Size, size)
	case underACL && !everyoneRuleGovernsRead(state.Rules):
		return fmt.Errorf("%s carries no effective %q rule after the upload — it would be readable by every account", relPath, ncRecordingsEveryoneGroup)
	}
	return nil
}

// readRemoteCatalog fetches the archive's authoritative index. missing reports
// that there is none yet — the only case in which the catalog leaf is about to
// be CREATED, and therefore could be born without rules.
func (s *nextcloudFilesPublishSink) readRemoteCatalog(ctx context.Context, root string) (catalog siteCatalog, missing bool, err error) {
	raw, status, err := s.cfg.davGetBytes(ctx, s.client, ncRecordingsOwner, root+"/catalog.json")
	switch {
	case err != nil && status != http.StatusNotFound:
		return siteCatalog{}, false, fmt.Errorf("read remote catalog: %w", err)
	case status == http.StatusNotFound:
		return siteCatalog{}, true, nil
	case status < 200 || status >= 300:
		// Branch on the status, never the error: davGetBytes returns nil for a 403
		// or a 503, and any JSON object without `meetings` parses as an empty
		// archive, which upsert would then write whole over every meeting.
		return siteCatalog{}, false, fmt.Errorf("read remote catalog: HTTP %d", status)
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		// Overwriting an unreadable catalog would drop the archive it indexes.
		return siteCatalog{}, false, fmt.Errorf("parse remote catalog: %w", err)
	}
	return catalog, false, nil
}

// upsertRemoteCatalog merges the delivered meetings into the catalog snapshot
// read before delivery and writes the protected index.
func (s *nextcloudFilesPublishSink) upsertRemoteCatalog(ctx context.Context, existing siteCatalog, catalogMissing bool, incoming siteCatalog, overlay catalogEntryOverlay, accessControlled bool, root string) error {
	catalogRemote := root + "/catalog.json"

	merged, err := upsertSiteCatalog(existing, incoming, overlay)
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
	// The catalog is reserved empty and denied like a recording: PUT outright on
	// a fresh archive it would be the unfiltered index of every meeting, with no
	// rules, inheriting `everyone: READ`. Not in the default model, where it is
	// the service account's own and the PROPPATCH would be rejected.
	if !accessControlled {
		if err := s.cfg.davPutBytes(ctx, s.client, ncRecordingsOwner, catalogRemote, body, "application/json"); err != nil {
			return fmt.Errorf("put catalog: %w", err)
		}
		return nil
	}
	if catalogMissing {
		if _, err := s.cfg.davPutEmpty(ctx, s.client, ncRecordingsOwner, catalogRemote, "application/json"); err != nil {
			return fmt.Errorf("reserve catalog: %w", err)
		}
		if err := s.cfg.davProppatchACLRules(ctx, s.client, ncRecordingsOwner, catalogRemote, catalogProtectionACLRules()); err != nil {
			return fmt.Errorf("protect catalog: %w", err)
		}
	}
	if err := s.cfg.davPutBytes(ctx, s.client, ncRecordingsOwner, catalogRemote, body, "application/json"); err != nil {
		return fmt.Errorf("put catalog: %w", err)
	}
	// The all-users group may traverse the containers but must not read the
	// unfiltered catalog; the operator reads it as the owner and filters it.
	if err := s.cfg.davProppatchACLRules(ctx, s.client, ncRecordingsOwner, catalogRemote, catalogProtectionACLRules()); err != nil {
		return fmt.Errorf("protect catalog: %w", err)
	}
	return nil
}

// davPutBytes uploads an in-memory body (the merged catalog).
func (c ExAppConfig) davPutBytes(ctx context.Context, client *http.Client, userID, relPath string, body []byte, contentType string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.davFileURL(userID, relPath), bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Content-Type", contentType)
	sum := sha256.Sum256(body)
	req.Header.Set("OC-Checksum", "SHA256:"+hex.EncodeToString(sum[:]))
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
// An ExApp gets nextcloud-files, so a production install whose
// CASSINI_PUBLISH_SINK never arrives — AppAPI only injects variables info.xml
// declares (D-403) — does not quietly keep recordings on the app's own volume.
// APP_SECRET is injected by AppAPI itself, so it cannot be dropped that way.
func defaultPublishSinkFor(exapp ExAppConfig) string {
	if exapp.Active {
		return publishSinkNextcloudFiles
	}
	return publishSinkLocal
}

// newPublishSinkFor builds the named sink, including the ones that need more
// than Config. Unknown names are rejected here too, since a resolved default
// has not been through loadConfig's validation.
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
			cassiniBin:  cfg.CassiniBin,
			rt:          rt,
		}, nil
	default:
		return newPublishSink(name, cfg, logger)
	}
}
