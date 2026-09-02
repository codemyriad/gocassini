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
	"path"
	"regexp"
	"strings"
)

// Moving an existing archive between the two storage models (D-616 first pass).
//
// The mechanics here are not invented; they are what the D-660 bench measured
// against a live Nextcloud 34 + Group Folders 22 (see
// _ivans-notes/development/660-mount-collision-spike/). Three findings shape
// every step below:
//
//  1. A Team folder mounted at `Cassini` WINS the canonical path, and the
//     service account's same-named home directory is physically renamed by the
//     server to `Cassini (N)`. The suffix is the server's choice, so it is
//     discovered, never assumed.
//  2. `MOVE` with `Overwrite: F` onto a fresh destination is 201 and preserves
//     the file's id, and a group-folder ACL PROPPATCHes onto it fine afterwards.
//  3. `MOVE` with `Overwrite: T` onto an existing destination is a data-loss
//     trap: for a file the destination's id (and therefore its ACL rows) is
//     destroyed, and for a DIRECTORY the server deletes the whole destination
//     tree first. Nothing here ever sets it.
//
//	default ──▶ access controlled            access controlled ──▶ default
//	─────────────────────────────            ─────────────────────────────
//	 stranded `Cassini (N)`/Recordings        `Cassini`/Recordings  (mounted)
//	          │  MOVE, Overwrite: F                   │  public ACL, then MOVE
//	          ▼                                       ▼
//	 `Cassini`/Recordings  (mounted)          `Cassini-optout`/Recordings
//	          │  PROPPATCH public                     │  unmap the folder's groups
//	          ▼                                       ▼  (it stops being mounted)
//	    everyone: READ                        MOVE `Cassini-optout` ──▶ `Cassini`
//
// Moved recordings are PUBLIC, by decision rather than by omission: the opt-in
// spec is explicit that a first pass does not infer a historical audience.
// Guessing one from today's Talk room would be a claim about who was in a
// meeting months ago that nothing in the archive can support.

// ncStorageStagingRoot is the deterministic name the opt-OUT parks the archive
// under while the Team folder is still mounted over `Cassini`.
//
// Deterministic on purpose. The server's own collision suffix — `Cassini (1)`,
// `(2)`, … — is what it picks, not what we asked for, so a transition that
// relied on it would have to guess which of several similarly named
// directories was its own. This name is ours, and finding it in the owner's
// root is unambiguous evidence that an opt-out did not finish.
const ncStorageStagingRoot = "Cassini-optout"

// ncCollisionSuffix matches the directory the server renames a colliding home
// tree to. The number is deliberately open: the suffix is server-chosen and the
// bench only happened to see `(1)`.
var ncCollisionSuffix = regexp.MustCompile(`^` + regexp.QuoteMeta(ncRecordingsMount) + ` \(\d+\)$`)

// errTransitionNotReady means the target mode's prerequisites are not there.
// The archive has not been touched.
var errTransitionNotReady = errors.New("the target storage mode is not ready")

// storageTransitionResult is what one transition did, so the UI can say more
// than "ok".
type storageTransitionResult struct {
	Mode            string   `json:"mode"`
	MeetingsMoved   int      `json:"meetings_moved"`
	CatalogMoved    bool     `json:"catalog_moved"`
	SourceRoot      string   `json:"source_root,omitempty"`
	DestinationRoot string   `json:"destination_root,omitempty"`
	LeftoverSource  string   `json:"leftover_source,omitempty"`
	UnmappedGroups  []string `json:"unmapped_groups,omitempty"`
}

// switchStorageMode moves the archive and records the new mode.
//
// It takes provisionMu for the whole operation, which is the same lock the
// enabled-edge preflight holds — so a transition and a preflight cannot
// interleave, and no publish can observe a half-moved archive under a mode that
// no longer describes it. The mode flag is written LAST, after the bytes have
// arrived: a crash mid-move leaves the recorded mode still describing where the
// recordings actually are.
func (c ExAppConfig) switchStorageMode(ctx context.Context, enableAccessControl bool, logger *log.Logger) (storageTransitionResult, error) {
	if !c.appAPIActive() {
		return storageTransitionResult{}, fmt.Errorf("storage mode can only be changed in a Nextcloud (AppAPI) deployment")
	}
	provisionMu.Lock()
	defer provisionMu.Unlock()

	client := &http.Client{Timeout: ncProvisionTimeout}
	probe, err := c.probeNCStorage(ctx, client, logger)
	if err != nil {
		return storageTransitionResult{}, fmt.Errorf("could not inspect this Nextcloud: %w", err)
	}
	if ready, step, detail := probe.sanityForTarget(enableAccessControl); !ready {
		return storageTransitionResult{}, fmt.Errorf("%w (%s): %s", errTransitionNotReady, step, detail)
	}

	var result storageTransitionResult
	if enableAccessControl {
		result, err = c.moveArchiveIntoTeamFolder(ctx, client, probe, logger)
	} else {
		result, err = c.moveArchiveOutOfTeamFolder(ctx, client, probe, logger)
	}
	if err != nil {
		return result, err
	}

	if path := ncStorage.settingsPath(); path != "" {
		if err := SaveStorageSettings(path, enableAccessControl); err != nil {
			// The bytes have already moved. Refusing now would leave the archive
			// in the new place under the old mode, which is worse than saying
			// what happened — so this is reported, not swallowed, and the caller
			// turns it into a visible error the administrator can act on.
			return result, fmt.Errorf("the archive was moved, but the new storage mode could not be saved to %s: %w — set it again once the volume is writable, or the next enable will move it back", path, err)
		}
	}
	ncStorage.set(enableAccessControl, storageModeSourceConfigured)
	result.Mode = storageModeName(enableAccessControl)

	// Re-run the preflight in the same critical section so /status, /setup and
	// the publish gate describe the archive as it is NOW, rather than as it was
	// before the move.
	c.preflightNCStorageLocked(ctx, client, logger)
	return result, nil
}

// sanityForTarget asks whether a mode could be switched TO, which is a
// different question from whether the mode currently in force is usable.
//
// The difference is the mount check. Running in default mode with a mounted
// Team folder is a mismatch worth refusing to publish under; switching INTO
// default mode is precisely the operation that clears it, so it must not be
// blocked by it.
func (p ncStorageProbe) sanityForTarget(accessControlled bool) (ok bool, step, detail string) {
	if accessControlled {
		return p.accessControlReady()
	}
	return p.defaultReady()
}

// moveArchiveIntoTeamFolder is the opt-IN. The administrator has already
// created the Team folder, which means the server has already renamed the
// private tree out of the canonical path — so the first job is finding it.
func (c ExAppConfig) moveArchiveIntoTeamFolder(ctx context.Context, client *http.Client, probe ncStorageProbe, logger *log.Logger) (storageTransitionResult, error) {
	result := storageTransitionResult{DestinationRoot: ncRecordingsRoot}

	source, err := c.findStrandedRecordingsRoot(ctx, client)
	if err != nil {
		return result, fmt.Errorf("look for an existing archive in %q's home: %w", ncRecordingsOwner, err)
	}
	result.SourceRoot = source

	// The destination tree, under an owner-only floor. Same ordering as the
	// preflight and for the same reason: nothing may be reachable through a
	// broad container grant before it states its own rules.
	if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, ncRecordingsMount, ownerOnlyContainerACLRules()); err != nil {
		return result, fmt.Errorf("apply the owner-only floor to %q: %w", ncRecordingsMount, err)
	}
	if err := c.mkcolRecordingsTree(ctx, client); err != nil {
		return result, err
	}

	if source != "" {
		moved, err := c.moveMeetings(ctx, client, source+"/meetings", ncRecordingsRoot+"/meetings", false, true, logger)
		result.MeetingsMoved = moved
		if err != nil {
			return result, err
		}
		catalogMoved, err := c.mergeCatalogInto(ctx, client, source+"/catalog.json", ncRecordingsRoot+"/catalog.json", true, logger)
		result.CatalogMoved = catalogMoved
		if err != nil {
			return result, err
		}
	}

	// Widen the root only now that every leaf under it states its own audience.
	if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, ncRecordingsMount, containerACLRules()); err != nil {
		return result, fmt.Errorf("grant %q read on %q: %w", ncRecordingsEveryoneGroup, ncRecordingsMount, err)
	}

	if source != "" {
		if left, err := c.removeEmptiedSource(ctx, client, source, logger); err != nil {
			logger.Printf("nc storage: could not remove the emptied %s: %v", source, err)
			result.LeftoverSource = left
		} else {
			result.LeftoverSource = left
		}
	}
	logger.Printf("nc storage: opted in to access control — moved %d recordings from %q into the %q Team folder", result.MeetingsMoved, source, ncRecordingsMount)
	return result, nil
}

// moveArchiveOutOfTeamFolder is the opt-OUT.
//
// Order is load-bearing: the recordings have to leave the Team folder while it
// is still mounted, because unmapping its groups is what makes it disappear
// from the service account's Files — including from the account doing the
// moving.
func (c ExAppConfig) moveArchiveOutOfTeamFolder(ctx context.Context, client *http.Client, probe ncStorageProbe, logger *log.Logger) (storageTransitionResult, error) {
	result := storageTransitionResult{SourceRoot: ncRecordingsRoot, DestinationRoot: ncRecordingsRoot}

	if !probe.FolderMounted {
		// Nothing is mounted over the canonical path, so this instance is
		// already storing recordings the default way and only the flag is out
		// of step. Make the tree and stop — moving nothing is the correct
		// amount of moving.
		if err := c.mkcolRecordingsTree(ctx, client); err != nil {
			return result, err
		}
		logger.Printf("nc storage: opted out of access control; no %q Team folder is mounted, so nothing had to be moved", ncRecordingsMount)
		return result, nil
	}

	staging := ncStorageStagingRoot + "/Recordings"
	for _, dir := range recordingsTreeDirs(staging) {
		if err := c.davMkcol(ctx, client, ncRecordingsOwner, dir); err != nil {
			return result, fmt.Errorf("mkcol %s: %w", dir, err)
		}
	}

	// 1. Out of the Team folder, while it is still mounted. Each leaf is made
	//    public on the way — the only moment its rules are settable at all.
	moved, err := c.moveMeetings(ctx, client, ncRecordingsRoot+"/meetings", staging+"/meetings", true, false, logger)
	result.MeetingsMoved = moved
	if err != nil {
		return result, err
	}
	catalogMoved, err := c.mergeCatalogInto(ctx, client, ncRecordingsRoot+"/catalog.json", staging+"/catalog.json", false, logger)
	result.CatalogMoved = catalogMoved
	if err != nil {
		return result, err
	}

	// 2. Refuse to unmap a folder that still holds recordings. Unmapping it
	//    would make them unreachable to every account including this one, and
	//    the administrator would be left with an empty-looking archive and no
	//    error at all.
	remaining, _, err := c.davPropfindNames(ctx, client, ncRecordingsOwner, ncRecordingsRoot+"/meetings")
	if err != nil {
		return result, fmt.Errorf("verify the Team folder is empty before unmapping it: %w", err)
	}
	if len(remaining) > 0 {
		return result, fmt.Errorf("%d recordings are still in the %q Team folder after the move, so it was left mounted and nothing was changed further; re-run the switch", len(remaining), ncRecordingsMount)
	}

	// 3. Unmap it. This is what makes the canonical path resolve to the service
	//    account's own home again.
	folderID, ok := probe.Folder.idInt()
	if !ok {
		return result, fmt.Errorf("the %q Team folder has no usable id, so its group mappings could not be removed", ncRecordingsMount)
	}
	for _, group := range []string{ncRecordingsEveryoneGroup, ncRecordingsOwnerGroup} {
		if !probe.Folder.hasGroup(group) {
			continue
		}
		if err := c.removeFolderGroup(ctx, client, folderID, group); err != nil {
			return result, fmt.Errorf("remove the %q mapping from the %q Team folder (the recordings are safe, in %q): %w", group, ncRecordingsMount, staging, err)
		}
		result.UnmappedGroups = append(result.UnmappedGroups, group)
	}

	// 4. Back to the canonical path, one leaf at a time rather than by renaming
	//    the staging directory onto it.
	//
	//    Renaming is the obvious move and it is the wrong one: `Cassini` in the
	//    service account's home is not reliably free. A previous opt-in leaves a
	//    server-renamed `Cassini (N)` behind, an administrator may have made one
	//    by hand, and a directory MOVE onto an existing destination either 412s
	//    (leaving the whole archive stranded under the staging name) or — with
	//    Overwrite — DELETES the destination tree first. Per-leaf moves merge
	//    into whatever is there, and a genuine name collision fails as one
	//    recording rather than as the archive.
	for _, dir := range recordingsTreeDirs(ncRecordingsRoot) {
		if err := c.davMkcol(ctx, client, ncRecordingsOwner, dir); err != nil {
			return result, fmt.Errorf("mkcol %s (the recordings are safe, in %q): %w", dir, staging, err)
		}
	}
	if _, err := c.moveMeetings(ctx, client, staging+"/meetings", ncRecordingsRoot+"/meetings", false, false, logger); err != nil {
		result.LeftoverSource = ncStorageStagingRoot
		return result, fmt.Errorf("%w — the recordings are safe under %q and the switch can be re-run", err, ncStorageStagingRoot)
	}
	if _, err := c.mergeCatalogInto(ctx, client, staging+"/catalog.json", ncRecordingsRoot+"/catalog.json", false, logger); err != nil {
		result.LeftoverSource = ncStorageStagingRoot
		return result, fmt.Errorf("%w — the recordings are safe under %q and the switch can be re-run", err, ncStorageStagingRoot)
	}
	if left, err := c.removeEmptiedSource(ctx, client, staging, logger); err != nil {
		logger.Printf("nc storage: could not remove the emptied %s: %v", staging, err)
		result.LeftoverSource = left
	} else {
		result.LeftoverSource = left
	}

	logger.Printf("nc storage: opted out of access control — moved %d recordings out of the %q Team folder into %q's own %s", result.MeetingsMoved, ncRecordingsMount, ncRecordingsOwner, ncRecordingsRoot)
	return result, nil
}

// moveMeetings moves every child of srcDir into dstDir, one MOVE per file, and
// returns how many arrived.
//
// ruleSource/ruleDestination say which SIDE of the move the public rule is
// written on, and only one of them is ever true, because `nc:acl-list` is
// settable inside the Team folder and nowhere else:
//
//	into the Team folder    ruleDestination. The destination is where rules
//	                        live, and the owner-only container floor is already
//	                        in force, so nothing is readable in between.
//	out of the Team folder   ruleSource. "Drop all ACLs, everything public
//	                        again" is the requirement, and while the leaf is
//	                        still inside the folder is the only moment it can be
//	                        stated at all — rather than relying on the rows
//	                        merely becoming inert once they leave.
//	within the home          neither. There is no folder on either side.
//
// Overwrite is never set. A destination that already exists answers 412 and the
// transition fails loudly, which is the right outcome: the alternative deletes
// the destination's id along with its rules (measured, D-660 part 2).
func (c ExAppConfig) moveMeetings(ctx context.Context, client *http.Client, srcDir, dstDir string, ruleSource, ruleDestination bool, logger *log.Logger) (int, error) {
	names, visible, err := c.davPropfindNames(ctx, client, ncRecordingsOwner, srcDir)
	if err != nil {
		return 0, fmt.Errorf("list %s: %w", srcDir, err)
	}
	if !visible {
		return 0, nil
	}
	moved := 0
	for _, name := range names {
		src := srcDir + "/" + name
		dst := dstDir + "/" + name
		if ruleSource {
			if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, src, publicRecordingACLRules()); err != nil {
				return moved, fmt.Errorf("make %s public before moving it: %w", src, err)
			}
		}
		if err := c.davMove(ctx, client, ncRecordingsOwner, src, dst, false); err != nil {
			return moved, fmt.Errorf("move %s to %s: %w", src, dst, err)
		}
		if ruleDestination {
			if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, dst, publicRecordingACLRules()); err != nil {
				return moved, fmt.Errorf("make %s readable after moving it: %w", dst, err)
			}
		}
		moved++
		if logger != nil {
			logger.Printf("nc storage: moved %s -> %s", src, dst)
		}
	}
	return moved, nil
}

// publicRecordingACLRules is what a migrated recording gets: readable by every
// account, writable by the owner.
//
// It is recordingACLRules(nil, true) — the same rule set a recording of a
// PUBLIC Talk conversation is published with, which is what makes this an
// existing, exercised shape rather than a new one. The alternative, inferring
// each meeting's original audience from its Talk room, is deliberately out of
// scope: the room's attendee list today is not evidence of who was in a call
// last quarter, and the archive carries nothing better.
func publicRecordingACLRules() []aclRule {
	return recordingACLRules(nil, true)
}

// mergeCatalogInto moves the archive's index, merging rather than replacing
// when both sides have one.
//
// Replacing would be the ordinary failure here and it is unrecoverable: the
// catalog is the only thing that makes a recording discoverable, upsert writes
// the merged document whole, and a later publish would append to whatever
// truncated file it found.
func (c ExAppConfig) mergeCatalogInto(ctx context.Context, client *http.Client, srcPath, dstPath string, intoTeamFolder bool, logger *log.Logger) (bool, error) {
	srcRaw, srcStatus, err := c.davGetBytes(ctx, client, ncRecordingsOwner, srcPath)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", srcPath, err)
	}
	if srcStatus == http.StatusNotFound {
		return false, nil
	}
	if srcStatus < 200 || srcStatus >= 300 {
		return false, fmt.Errorf("read %s -> HTTP %d", srcPath, srcStatus)
	}
	var source siteCatalog
	if err := json.Unmarshal(srcRaw, &source); err != nil {
		return false, fmt.Errorf("parse %s: %w", srcPath, err)
	}

	dstRaw, dstStatus, err := c.davGetBytes(ctx, client, ncRecordingsOwner, dstPath)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", dstPath, err)
	}
	var destination siteCatalog
	destinationExists := dstStatus >= 200 && dstStatus < 300
	if destinationExists {
		if err := json.Unmarshal(dstRaw, &destination); err != nil {
			return false, fmt.Errorf("parse %s: %w", dstPath, err)
		}
	} else if dstStatus != http.StatusNotFound {
		return false, fmt.Errorf("read %s -> HTTP %d", dstPath, dstStatus)
	}

	merged, err := upsertSiteCatalog(destination, source, catalogEntryOverlay{})
	if err != nil {
		return false, fmt.Errorf("merge %s into %s: %w", srcPath, dstPath, err)
	}
	if merged.Meetings == nil {
		merged.Meetings = []json.RawMessage{}
	}
	body, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal merged catalog: %w", err)
	}
	body = append(body, '\n')

	if intoTeamFolder && !destinationExists {
		// Same reservation as a first publish: the authoritative index of every
		// meeting on the instance must not exist inside the Team folder with no
		// rules of its own, inheriting the container grant.
		if _, err := c.davPutEmpty(ctx, client, ncRecordingsOwner, dstPath, "application/json"); err != nil {
			return false, fmt.Errorf("reserve %s: %w", dstPath, err)
		}
		if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, dstPath, catalogProtectionACLRules()); err != nil {
			return false, fmt.Errorf("protect %s: %w", dstPath, err)
		}
	}
	if err := c.davPutBytes(ctx, client, ncRecordingsOwner, dstPath, body, "application/json"); err != nil {
		return false, fmt.Errorf("write %s: %w", dstPath, err)
	}
	if intoTeamFolder {
		if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, dstPath, catalogProtectionACLRules()); err != nil {
			return false, fmt.Errorf("protect %s: %w", dstPath, err)
		}
	}
	if err := c.davDelete(ctx, client, ncRecordingsOwner, srcPath); err != nil {
		// The index has already arrived; a leftover copy of it is untidy, not
		// wrong, and it is inside the service account's own tree either way.
		if logger != nil {
			logger.Printf("nc storage: merged catalog into %s but could not remove %s: %v", dstPath, srcPath, err)
		}
	}
	return true, nil
}

// removeEmptiedSource deletes the directory the archive came from, but only
// once it is provably empty. It returns the path when something was left
// behind, so the caller can say where.
func (c ExAppConfig) removeEmptiedSource(ctx context.Context, client *http.Client, sourceRoot string, logger *log.Logger) (string, error) {
	remaining, visible, err := c.davPropfindNames(ctx, client, ncRecordingsOwner, sourceRoot+"/meetings")
	if err != nil {
		return sourceRoot, err
	}
	if visible && len(remaining) > 0 {
		if logger != nil {
			logger.Printf("nc storage: leaving %s in place — it still holds %d recordings", sourceRoot, len(remaining))
		}
		return sourceRoot, nil
	}
	parent := path.Dir(strings.Trim(sourceRoot, "/"))
	if parent == "." || parent == "" {
		parent = sourceRoot
	}
	if err := c.davDelete(ctx, client, ncRecordingsOwner, parent); err != nil {
		return sourceRoot, err
	}
	return "", nil
}

// findStrandedRecordingsRoot looks for an archive sitting outside the canonical
// path in the service account's home, and returns its Recordings root ("" when
// there is none).
//
// It exists because the opt-in cannot control the order. The administrator
// creates the Team folder by hand — that is the whole point of a first pass
// that scaffolds nothing — and at that moment the server renames the colliding
// home directory to a name of its own choosing. So the tree has to be
// discovered rather than remembered, and the suffix is matched as a pattern
// because the bench's `(1)` is the server's choice, not a contract.
//
// The staging name an interrupted opt-out leaves behind is checked first: it is
// ours, so it is unambiguous, and finding it means the last transition did not
// finish.
func (c ExAppConfig) findStrandedRecordingsRoot(ctx context.Context, client *http.Client) (string, error) {
	children, visible, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, "")
	if err != nil {
		return "", err
	}
	if !visible {
		return "", nil
	}
	var candidates []string
	for _, name := range children {
		if name == ncStorageStagingRoot {
			candidates = append([]string{name}, candidates...)
			continue
		}
		if ncCollisionSuffix.MatchString(name) {
			candidates = append(candidates, name)
		}
	}
	for _, candidate := range candidates {
		root := candidate + "/Recordings"
		if _, ok, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, root); err == nil && ok {
			return root, nil
		}
	}
	return "", nil
}

// davMove moves relPath to destination within one account's Files.
//
// Overwrite is exposed but every caller passes false, and the header is sent
// explicitly rather than left to the server default because the default IS
// true: an omitted header on an existing destination silently destroys it,
// which for a directory means the whole tree (measured, D-660 part 2).
func (c ExAppConfig) davMove(ctx context.Context, client *http.Client, userID, relPath, destination string, overwrite bool) error {
	req, err := http.NewRequestWithContext(ctx, "MOVE", c.davFileURL(userID, relPath), nil)
	if err != nil {
		return err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Destination", c.davFileURL(userID, destination))
	if overwrite {
		req.Header.Set("Overwrite", "T")
	} else {
		req.Header.Set("Overwrite", "F")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusPreconditionFailed {
		return fmt.Errorf("MOVE %s -> %s: %s already exists (refusing to overwrite it)", relPath, destination, destination)
	}
	return fmt.Errorf("MOVE %s -> %s: HTTP %d", relPath, destination, resp.StatusCode)
}

// davPropfindChildren lists the immediate children of relDir as userID and
// returns their basenames, excluding the collection itself.
//
// davPropfindNames answers the neighbouring question — which `.opus` files may
// this caller see — and filtering by extension is exactly wrong here: the
// things being looked for are DIRECTORIES with server-chosen names.
func (c ExAppConfig) davPropfindChildren(ctx context.Context, client *http.Client, userID, relDir string) (names []string, visible bool, err error) {
	reqBody := []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>`)
	selfURL := c.davFileURL(userID, relDir)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", selfURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, false, err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", ncFilesACLMediaType)
	req.ContentLength = int64(len(reqBody))
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("PROPFIND %s -> %d", relDir, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, false, err
	}
	var ms struct {
		Responses []struct {
			Href string `xml:"href"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(body, &ms); err != nil {
		return nil, false, fmt.Errorf("parse multistatus: %w", err)
	}
	// The collection lists itself first. Comparing decoded PATHS rather than
	// basenames is what keeps a child that happens to share the collection's
	// name — or the account's, when listing the home root — from being dropped.
	selfPath := ""
	if parsed, perr := url.Parse(selfURL); perr == nil {
		selfPath = path.Clean(parsed.Path)
	}
	out := make([]string, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		href := strings.TrimRight(r.Href, "/")
		decoded := href
		if unescaped, derr := url.PathUnescape(href); derr == nil {
			decoded = unescaped
		}
		if selfPath != "" && path.Clean(decoded) == selfPath {
			continue
		}
		base := path.Base(decoded)
		if base == "" || base == "." || base == "/" {
			continue
		}
		out = append(out, base)
	}
	return out, true, nil
}
