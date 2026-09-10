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
	"sync"
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
// Steps 4-6 are ordered the way they are because of D-594: the object must never
// be reachable with content in it and no rules on it, and it must never be
// advertised before its audience is frozen. The bytes therefore land last within
// a leaf, and the catalog last overall.
//
// Step 3 is a read, and it is where it is for a different reason: the index is
// the only durable evidence of whether a meeting's audience was ever written, and
// step 4 needs that to tell an unfinished publish from an administrator's
// deliberate narrowing.
//
// Step 6 reads-merges-writes rather than uploading the local catalog, and that
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
	// cassiniBin is the CLI a re-delivery carries the delivered copy's marks
	// forward with (D-737). Empty means no CLI: the recording is delivered as it
	// always was, which loses nothing, because the routes that write marks are
	// not mounted without one either (newAnnotationService).
	cassiniBin string
	// annotations is the marks projection, read at delivery time rather than
	// captured at construction so the order the Runtime is assembled in does not
	// matter. Nil, or answering nil, means there is no projection to update.
	annotations func() annotationIndex
	// noCLIOnce keeps "marks are not carried here" to one line per process.
	noCLIOnce sync.Once
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
		return "", fmt.Errorf("the recordings storage is not ready (%s: %s); refusing to publish — see GET /status recordings_access and the Setup tab in the Cassini app, or set %s=local to keep recordings on this app's own volume",
			snap.Step, snap.Detail, envPublishSinkName)
	}
	// A delivery and a storage-mode switch may not overlap.
	//
	// Reading the mode once was enough while a switch only MOVED files: the
	// worst case was an asset written under the old mode a moment before the
	// flip. It stopped being enough when the switch gained its final step. A
	// delivery that lands in root(X) after the switch has copied and verified,
	// but before it empties root(X), is deleted — and the job has already been
	// marked succeeded and its staging copy removed, so the recording is simply
	// gone. The window is as long as a WebDAV DELETE pass over the whole archive.
	//
	// provisionMu is the existing "nothing else is touching the archive" lock,
	// held by the switch, the recovery and the enabled-edge preflight. Taking it
	// here is what makes the switch's own claim — that no publish can observe a
	// half-copied archive under a mode that no longer describes it — actually
	// true. Nothing below reaches back into it, so there is no re-entrancy.
	provisionMu.Lock()
	defer provisionMu.Unlock()

	// Which storage model this archive is under (D-616). Read ONCE, here, and now
	// under the lock, so a mode that changes mid-delivery cannot leave one asset
	// ruled and the next one not — nor one asset in each tree. The gate above
	// guarantees it is resolved: the substrate cannot report `provisioned` before
	// the preflight decided.
	accessControlled := ncStorage.accessControlled()
	// ...and therefore WHERE it goes. The two models have separate roots so that
	// neither can shadow the other (nc_storage_paths.go); reading the mode once
	// and deriving the root from it here is what keeps a delivery from putting
	// one asset in each tree.
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

	// The audience is written once, onto the meeting's .opus, and only when that
	// leaf has not already got one. Which upload that is has to be decided here,
	// against the same path ncFilesAccessApplier will target.
	//
	// The coupling is structural — attemptOpusPath hardcodes <jobID>.opus and the
	// exporter derives both the catalog id and audioPath from that stem — so it
	// takes a code change to break, and it fails CLOSED when broken:
	// createProtectedLeaf still writes the owner-only deny unconditionally, so a
	// drifted asset publishes over-restricted rather than unruled. It is the
	// installed-ExApp e2e that would catch it, not the tests in this package:
	// writeAttemptSite fabricates the site with the same convention the sink
	// assumes, so a unit test cannot disagree with it.
	opusRemote := root + "/meetings/" + d.JobID + ".opus"
	audienceNeeded := false

	// Read the archive's index BEFORE touching anything, because it is the only
	// evidence available for a question the leaf's rules cannot answer on their
	// own: has this meeting's audience ever been written?
	//
	// A leaf sitting at exactly the owner-only baseline is ambiguous. It is what
	// a publish that died between the content PUT and the audience PROPPATCH
	// leaves — and it is equally what an administrator leaves by narrowing a
	// recording to owner-only from the Files UI, which the operator docs invite
	// them to do. Re-deriving the audience in the second case is a silent revert
	// of a deliberate access decision, and for a recording of a PUBLIC room it
	// re-grants `everyone` read on something someone had just made private.
	//
	// The catalog settles it. It is written last, so a meeting that appears in it
	// got through the audience step; one that does not, did not.
	existingCatalog, catalogMissing, err := s.readRemoteCatalog(ctx, root)
	if err != nil {
		return "", err
	}
	alreadyIndexed := catalogNamesMeeting(existingCatalog, d.JobID)

	// What each re-delivered recording now carries, as carry reported it, for
	// the projection once the delivery is complete (step 7).
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

	// Freezing the audience comes BEFORE the catalog, so a meeting whose ACL did
	// not land is never advertised — the catalog is the thing that makes a
	// recording discoverable, and writing it first is what let a failed ACL
	// produce an indexed, readable recording.
	//
	// In the default model there is no audience to freeze. The tree is the
	// service account's own, nobody else has a mount of it, and the operator
	// serves every caller the whole archive as the owner — so a per-file ACL
	// would restrict nothing and `nc:acl-list` is not even a settable property
	// outside a Team folder with advanced ACL (it answers 207 with a 403
	// propstat, which is how a recording gets written with no rules at all).
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

// catalogNamesMeeting reports whether the archive's index already lists jobID.
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
//
// Wherever the leaf is present, "bytes" means the sealed audio plus the marks
// the delivered copy carries, not the sealed file alone (D-737): marks written
// since the last publish live only on the delivered copy, so replacing it with
// the sealed file erases them. See putOverDeliveredCopy. marks is carry's report
// of what was delivered, nil where nothing was carried.
//
// None of it applies in the default model (D-616). There the whole reason for
// the dance is absent: the tree is private to the service account, no other
// account has a mount of it, and nothing about a leaf's existence discloses
// anything to anybody. Writing the bytes IS the delivery. Reserving an empty
// file to PROPPATCH rules onto it would not merely be wasted work — the
// PROPPATCH would be REJECTED, because `nc:acl-list` is only settable inside a
// Team folder with advanced ACL, and every publish would fail.
func (s *nextcloudFilesPublishSink) deliverAsset(ctx context.Context, item upload, alreadyIndexed, accessControlled bool) (audienceNeeded bool, marks *annotateResult, err error) {
	if !accessControlled {
		if !s.carriesMarks(item) {
			return false, nil, s.putAssetBytes(ctx, item, accessControlled)
		}
		// This model has no health gate, so the one thing a re-delivery needs to
		// know — is a delivered copy there, and at which version — is asked here,
		// and only for a leaf that could be carrying marks.
		state, err := s.cfg.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, item.remote)
		if err != nil {
			return false, nil, fmt.Errorf("inspect %s: %w", item.remote, err)
		}
		if !state.Exists {
			return false, nil, s.putAssetBytes(ctx, item, accessControlled)
		}
		marks, err := s.putOverDeliveredCopy(ctx, item, state, accessControlled)
		return false, marks, err
	}
	if item.isDir {
		// A legacy artifactPath export is a directory, which has no single leaf
		// to reserve, no length to verify, and — the reason for the early exit
		// rather than a few skipped steps — nothing that should ever be fed to
		// the repair branch, where a missing rule set would DELETE the tree.
		// This asset shape is carried exactly as it was before D-594.
		return false, nil, s.putAssetBytes(ctx, item, accessControlled)
	}

	state, err := s.cfg.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, item.remote)
	if err != nil {
		return false, nil, fmt.Errorf("inspect %s: %w", item.remote, err)
	}

	// content is what the final PUT sends: the sealed file, unless the repair
	// below is about to delete a delivered copy whose marks must survive it.
	content := item
	switch {
	case state.Exists && !everyoneRuleGovernsRead(state.Rules):
		// The D-594 state itself: a delivered recording carrying no broad-group
		// rule, readable by every account. Repair it before replacing it —
		// deleting it first would only move the exposure into a trash every
		// account can read.
		//
		// Its marks are carried out FIRST, because the repair deletes the leaf
		// and every mark on it. The content PUT that follows is unconditional:
		// the leaf is then the empty reservation createProtectedLeaf made, and
		// no writer can commit a mark to an empty file. What this cannot see is
		// a mark committed between the fetch and the DELETE — a window of two
		// requests, on a leaf that is already in a state nothing should leave it
		// in (selfHealLeafProtection rules such leaves in place at startup).
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
		// Already protected. Whether the audience still has to be written is the
		// difference between "the create-time deny is all that ever landed" and
		// "this meeting's audience is frozen" — see audienceApplied.
		//
		// alreadyIndexed breaks the tie the rules alone cannot: a leaf at exactly
		// the baseline is both what an unfinished publish leaves and what an
		// administrator leaves by narrowing a recording to owner-only. If the
		// meeting is in the catalog, a previous publish got past the audience
		// step, so the baseline is somebody's decision and not our unfinished
		// work — leave it alone.
		audienceNeeded = !audienceApplied(state.Rules) && !alreadyIndexed
		if s.carriesMarks(item) {
			// Content only, still: carrying marks is a PUT, never a PROPPATCH, so
			// "a re-delivery replaces content, never access" holds unchanged.
			marks, err := s.putOverDeliveredCopy(ctx, item, state, accessControlled)
			if err != nil {
				return false, nil, err
			}
			return audienceNeeded, marks, nil
		}
	}

	if err := s.putAssetBytes(ctx, content, accessControlled); err != nil {
		return false, nil, err
	}
	return audienceNeeded, marks, nil
}

// maxCarryAttempts bounds how often a re-delivery re-reads a recording that
// keeps changing under it. Three, as the write endpoint uses: a person marking
// by hand does not commit three times inside one fetch-carry-PUT, so a third
// refusal means something is writing continuously and a later rerun is the
// better moment.
const maxCarryAttempts = 3

// putOverDeliveredCopy re-delivers item over a recording that is already in the
// archive, carrying the marks written on it since it was delivered (D-737).
//
// Marks exist ONLY on the delivered copy — the write endpoint rewrites that file
// and nothing else — so a re-delivery of the sealed file on its own silently
// erases every one of them. It is D-640's trap again: "writing it into
// catalog.json alone does not last; the exporter re-derives on every
// republish."
//
//	PROPFIND   ETag E                  (the health gate's, on the first attempt)
//	GET        the delivered copy
//	carry      its marks → a staged copy of the sealed file
//	PUT        the staged copy, If-Match: E
//	  412  →   someone committed a mark after the GET: start again, ≤ 3 times
//
// If-Match is what keeps this from being a lost update of its own. Without it a
// mark committed between the GET and the PUT would be overwritten by a staged
// copy that never saw it — the same erasure, just narrower.
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
				// Removed by hand mid-rerun. Re-deriving its access from here
				// would be guessing; the next publish takes the absent branch.
				return nil, fmt.Errorf("refusing to publish %s: it disappeared while this rerun was carrying its marks forward; re-run the publish", item.remote)
			}
		}
		if state.ETag == "" {
			// Sabre always reports one. Without it the PUT could only be
			// unconditional, which is the lost update If-Match is here to stop.
			return nil, fmt.Errorf("refusing to publish %s: Nextcloud reported no ETag for it, so it cannot be replaced without risking a mark written meanwhile; re-run the publish", item.remote)
		}
		staged, result, err := s.stageDeliveredMarks(ctx, item, dir)
		if err != nil {
			return nil, err
		}
		err = s.putAssetBytesIfMatch(ctx, staged, accessControlled, state.ETag)
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
// marks into a staged copy of the sealed file. It answers the staged copy —
// what the PUT sends, and what the size post-check compares against — and
// carry's report of it.
//
// The sealed file is never modified: carry reads it and writes a copy. So the
// seal preflight in Deliver, which ran before any of this, still proves exactly
// what it always did, that the audio leaving is the audio this job sealed; and
// carry's own verification proves the rest, that the staged copy is that audio
// unchanged plus the delivered copy's marks. Together: the bytes leaving are the
// sealed audio plus the marks already delivered (spec/cassini-opus-audio-
// integrity-v1.md, "Three digests").
func (s *nextcloudFilesPublishSink) stageDeliveredMarks(ctx context.Context, item upload, dir string) (upload, annotateResult, error) {
	delivered := filepath.Join(dir, "delivered.opus")
	staged := filepath.Join(dir, "staged.opus")
	// A retry reuses the directory, and nothing of the previous attempt may leak
	// into this one — least of all a staged copy of marks read at a stale ETag.
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
		// Silvio's rule: marks made against different audio are kept, with the
		// binding they were made under, and flagged — never dropped, and never
		// redrawn against audio they were not made for. So this is a delivery,
		// not a failure. Logged because somebody will ask why their ranges
		// stopped resolving after a rerun.
		s.logf("nc files: %s: %d mark(s) on the delivered copy were made against different audio — carried unresolved and delivered", item.remote, result.Carried)
	}
	return upload{local: staged, remote: item.remote, size: info.Size()}, result, nil
}

// sealedIfDeliveredIsUnreadable decides what a failed carry means.
//
// Exactly one failure is safe to deliver past: the delivered copy is not a
// recording at all — the zero-byte reservation a first publish leaves when it
// dies before its content lands, or bytes an interrupted upload truncated.
// Nothing could have committed a mark into either (apply cannot read them), so
// there is nothing to carry, and the sealed file goes as it is — still under
// If-Match. Refusing instead would wedge the meeting: every rerun would fetch
// the same unreadable copy and fail on it forever.
//
// Anything else fails the publish, because delivering past it is precisely the
// silent loss of every mark this exists to prevent. The CLI is asked to read
// each file on its own to tell the two apart: the delivered copy must be
// unreadable AND the sealed file readable, which puts the fault in the
// delivered bytes rather than in the CLI.
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

// carriesMarks reports whether a re-delivery of item has marks to carry forward:
// only a recording carries them, and only the CLI can move them.
func (s *nextcloudFilesPublishSink) carriesMarks(item upload) bool {
	if item.isDir || !strings.HasSuffix(item.remote, ".opus") {
		return false
	}
	if strings.TrimSpace(s.cassiniBin) == "" {
		s.noCLIOnce.Do(func() {
			s.logf("nc files: no cassini binary configured — a re-delivery replaces a recording without carrying marks forward; none can exist here, because the routes that write them are not mounted without one")
		})
		return false
	}
	return true
}

// newCarryDir makes the scratch directory a re-delivery holds the delivered copy
// and the staged one in. Beside the sealed file rather than in the system temp
// dir: on the operator's own volume like the recording itself (a /tmp on tmpfs
// would hold two recordings in memory), and inside the attempt site, which a
// successful publish removes anyway. Removed on return either way — the app
// keeps no copy of a delivered recording (D-550).
func newCarryDir(item upload) (string, func(), error) {
	dir, err := os.MkdirTemp(filepath.Dir(item.local), ".carry-")
	if err != nil {
		return "", nil, fmt.Errorf("carry scratch for %s: %w", item.remote, err)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// publishAnnotationsUnreadable is the projection's reason for a delivered
// recording whose marks could not be read back or recorded.
const publishAnnotationsUnreadable = "marks-unreadable"

// indexDeliveredMarks records what each delivered recording now carries in the
// marks projection (D-737), once the delivery is complete — the file first, the
// projection second, the order a mark write commits in.
//
// A re-delivery already has carry's report of the staged copy it delivered. A
// first publish delivered the sealed file itself, which is still on disk, so it
// is read there rather than fetched back. Nothing here can fail a publish: the
// projection is rebuildable, and a recording it could not read is recorded
// unavailable, so the vocabulary reports partial coverage rather than a false
// complete one.
func (s *nextcloudFilesPublishSink) indexDeliveredMarks(ctx context.Context, uploads []upload, carried map[string]annotateResult) {
	if s.annotations == nil {
		return
	}
	index := s.annotations()
	if index == nil {
		return
	}
	for _, item := range uploads {
		if item.isDir || !strings.HasSuffix(item.remote, ".opus") {
			continue
		}
		// The projection's join key: the delivered leaf's basename, the same
		// string the per-caller visibility scan returns.
		name := path.Base(item.remote)
		result, ok := carried[item.remote]
		if !ok {
			if strings.TrimSpace(s.cassiniBin) == "" {
				// Nothing to read marks with — and, for the same reason, none to read.
				continue
			}
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
func (s *nextcloudFilesPublishSink) putAssetBytes(ctx context.Context, item upload, accessControlled bool) error {
	return s.putAssetBytesIfMatch(ctx, item, accessControlled, "")
}

// putAssetBytesIfMatch is putAssetBytes made conditional on the stored copy
// still carrying ifMatch (when set). A refusal wraps errDAVPreconditionFailed
// and has written nothing, so the read-back below never runs for it.
func (s *nextcloudFilesPublishSink) putAssetBytesIfMatch(ctx context.Context, item upload, accessControlled bool, ifMatch string) error {
	if _, _, err := s.cfg.davPutFileIfMatch(ctx, s.client, ncRecordingsOwner, item.remote, item.local, ncRecordingsContentType, ifMatch); err != nil {
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
	// The same response already carries the leaf's rules, so checking them costs
	// nothing and closes the gap between the health gate and this PUT. If the
	// leaf lost its rules in between — an administrator deleted the recording
	// from the Files UI, a trash restore re-created it, a rule row went away —
	// then this PUT created a NEW leaf, with a new fileid and therefore no rules
	// at all, now holding the audio. Fail rather than return to a caller that
	// would go on to advertise it.
	//
	// Only under access control. In the default model a leaf HAS no rules by
	// design, so this exact check would fail every publish — and what it is
	// guarding against, a recording readable by every account, is the model.
	if accessControlled && !everyoneRuleGovernsRead(state.Rules) {
		return fmt.Errorf("refusing to publish %s: it carries no effective %q rule after the upload — it would be readable by every account; re-run the publish",
			item.remote, ncRecordingsEveryoneGroup)
	}
	return nil
}

// readRemoteCatalog fetches the archive's authoritative index. missing reports
// that there is none yet — the only case in which the catalog leaf is about to
// be CREATED, and therefore the only one in which it could be born without rules.
func (s *nextcloudFilesPublishSink) readRemoteCatalog(ctx context.Context, root string) (catalog siteCatalog, missing bool, err error) {
	raw, status, err := s.cfg.davGetBytes(ctx, s.client, ncRecordingsOwner, root+"/catalog.json")
	switch {
	case err != nil && status != http.StatusNotFound:
		return siteCatalog{}, false, fmt.Errorf("read remote catalog: %w", err)
	case status == http.StatusNotFound:
		return siteCatalog{}, true, nil
	case status < 200 || status >= 300:
		// Branch on the status, never on the error — davGetBytes returns a nil
		// error for a 403 or a 503, so "err == nil" does not mean "this is the
		// catalog". The rule guardDestinationIsEmpty already follows; this was
		// the one of the package's three catalog GETs that did not.
		//
		// What it costs to get wrong is the whole index, not one meeting's
		// audience. Meetings is []json.RawMessage, so ANY JSON object without a
		// `meetings` key parses cleanly as an empty archive — and upsert writes
		// the merged document whole, so one bad read replaces every meeting in
		// the archive with the one being delivered. Nothing repairs it: later
		// publishes append to the truncated file and backfill refuses a
		// populated destination.
		return siteCatalog{}, false, fmt.Errorf("read remote catalog: HTTP %d", status)
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		// Overwriting an unreadable catalog would silently drop the archive it
		// indexes. Refuse and leave it for a human.
		return siteCatalog{}, false, fmt.Errorf("parse remote catalog: %w", err)
	}
	return catalog, false, nil
}

// upsertRemoteCatalog merges the delivered meetings into the catalog snapshot
// read before delivery, preserving the archive and applying the operator's
// catalog-only fields before it writes the protected index.
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
	// The catalog gets the same treatment as a recording, and for the same
	// reason: on a first publish into a fresh archive this leaf does not exist
	// yet, so PUTting it outright would create the authoritative, unfiltered
	// index of every meeting on the instance — ids, titles, dates — with no
	// rules of its own, inheriting the container's `everyone: READ` until the
	// PROPPATCH below lands. Reserve it empty and deny it first; the rules then
	// survive this and every later overwrite, because the fileid does.
	// In the default model none of this applies: the catalog lives in the
	// service account's own home, no other account has a path to it, and the
	// operator is the only reader. The PROPPATCHes below would be rejected
	// outside a Team folder with advanced ACL, so skipping them is not a
	// weakening — it is the difference between publishing and failing.
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
			cassiniBin:  cfg.CassiniBin,
			annotations: func() annotationIndex {
				if rt == nil {
					return nil
				}
				return rt.annotations
			},
		}, nil
	default:
		return newPublishSink(name, cfg, logger)
	}
}
