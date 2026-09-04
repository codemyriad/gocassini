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

// Moving an existing archive between the two storage models (D-616 followups).
//
// The first pass MOVED the archive, because it had to: both models addressed
// `Cassini/Recordings`, so the source path was about to become the destination
// path and there was nowhere for the source to keep existing. That single
// constraint produced everything QA hit — a source discovered by regex, a
// staging directory, a Team-folder unmap that Nextcloud refuses to an ExApp, and
// an archive that a failure halfway left in neither place a reader looks.
//
// With one root per model (nc_storage_paths.go) the transition is a COPY, and
// the whole shape follows from that:
//
//	default ──▶ access controlled           access controlled ──▶ default
//	───────────────────────────────         ───────────────────────────────
//	 CassiniNoACL/Recordings                 Cassini/Recordings  (Team folder)
//	          │  COPY, Overwrite: F                   │  COPY, Overwrite: F
//	          ▼                                       ▼
//	 Cassini/Recordings  (Team folder)       CassiniNoACL/Recordings
//	          │  PROPPATCH public per leaf            │  nothing: a copy into the
//	          ▼                                       ▼  home gets a new fileid
//	   flip the mode, then EMPTY the source     outside any group folder, and
//	                                           groupfolders keys rules by fileid
//
// The source is only READ until the mode has flipped, so at every instant the
// recorded mode names a root holding a complete archive. That sentence is the
// invariant; `migration_clean` is what makes it survive a crash, and
// finishMigration is what repairs the one thing a crash can leave behind.
//
// Two mechanics are kept verbatim from the D-660 bench, because they are
// measured rather than assumed:
//
//  1. `Overwrite` is never `T`. For a file it destroys the destination's id and
//     therefore its ACL rows; for a DIRECTORY the server deletes the whole
//     destination tree first.
//  2. Rules attach to a leaf inside the Team folder after it arrives, and they
//     read back and enforce. Outside a Team folder `nc:acl-list` is not settable
//     at all — 500 with groupfolders installed, a FALSE 207 without it — which is
//     why the opt-out writes no rules rather than "clearing" them.
//
// Migrated recordings are PUBLIC, by decision rather than by omission: the
// opt-in spec is explicit that a first pass does not infer a historical
// audience. Guessing one from today's Talk room would be a claim about who was
// in a meeting months ago that nothing in the archive can support.

// ncStorageStagingRoot is the deterministic name the FIRST PASS's opt-out parked
// the archive under while the Team folder was still mounted over `Cassini`.
//
// Nothing writes it any more — the opt-out has a real destination now. It
// survives as a name the legacy adoption knows to look for, because an install
// whose first-pass opt-out died between unmapping the folder and carrying the
// archive back has its recordings sitting under exactly this name.
const ncStorageStagingRoot = "Cassini-optout"

// ncCollisionSuffix matches the directory the server renames a colliding home
// tree to. The number is deliberately open: the suffix is server-chosen and the
// D-660 bench only happened to see `(1)`.
//
// Also legacy-only. It exists because installs from the first pass may have
// their default-mode archive under `Cassini (N)/Recordings`, renamed out of the
// way when their Team folder was created. Nothing Cassini writes can collide any
// more.
var ncCollisionSuffix = regexp.MustCompile(`^` + regexp.QuoteMeta(ncRecordingsMount) + ` \(\d+\)$`)

// errTransitionNotReady means the target mode's prerequisites are not there.
// The archive has not been touched.
var errTransitionNotReady = errors.New("the target storage mode is not ready")

// storageTransitionResult is what one transition did, so the UI can say more
// than "ok".
type storageTransitionResult struct {
	Mode            string `json:"mode"`
	MeetingsMoved   int    `json:"meetings_moved"`
	CatalogMoved    bool   `json:"catalog_moved"`
	SourceRoot      string `json:"source_root,omitempty"`
	DestinationRoot string `json:"destination_root,omitempty"`
	// MeetingsAlreadyThere is how many of the source's recordings were already at
	// the destination and were therefore not copied again. Non-zero on a re-run
	// after a partial failure, which is the case worth naming out loud.
	MeetingsAlreadyThere int `json:"meetings_already_there,omitempty"`
	// SourceCleared says the source's contents were removed. False means the
	// archive arrived but the tidy-up did not finish, `migration_clean` is false,
	// and the Setup tab has a button for it.
	SourceCleared bool `json:"source_cleared"`
	// LeftoverSource names the root still holding a copy, when SourceCleared is
	// false.
	LeftoverSource string `json:"leftover_source,omitempty"`
}

// switchStorageMode copies the archive into the target model's root and records
// the new mode.
//
// It takes provisionMu for the whole operation, which is the same lock the
// enabled-edge preflight holds — so a transition and a preflight cannot
// interleave, and no publish can observe a half-copied archive under a mode that
// no longer describes it.
//
// The order below is the state machine, and every step of it is chosen so that a
// process killed at that instant leaves the recorded mode naming a root that
// holds a complete archive:
//
//  1. mark dirty            {mode: current, clean: false}   before any write
//  2. build the destination MKCOL, and under access control the owner-only floor
//  3. copy                  every meeting the destination does not already have
//  4. merge the catalog     never replace: it is the only index there is
//  5. widen + verify        every source meeting is at the destination
//  6. FLIP                  {mode: target, clean: false}    one write
//  7. empty the source      contents only; the collections stay
//  8. mark clean            {mode: target, clean: true}
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
	return c.migrateStorageLocked(ctx, client, enableAccessControl, logger)
}

// migrateStorageLocked is the sequence itself, with provisionMu held and the
// target's prerequisites already confirmed.
func (c ExAppConfig) migrateStorageLocked(ctx context.Context, client *http.Client, enableAccessControl bool, logger *log.Logger) (storageTransitionResult, error) {
	current, _ := ncStorage.mode()
	source := recordingsRootFor(current)
	destination := recordingsRootFor(enableAccessControl)

	result := storageTransitionResult{
		Mode:            storageModeName(current),
		SourceRoot:      source,
		DestinationRoot: destination,
	}

	// 1. Dirty BEFORE the first write, not after it. A process killed between the
	//    first MKCOL and this line would leave a directory nobody accounted for;
	//    killed after it, the leftovers are already claimed by the recovery.
	if err := c.recordStorageMode(current, storageModeSourceUser, false, logger); err != nil {
		return result, fmt.Errorf("could not record that a storage migration is in progress, so nothing was moved: %w", err)
	}

	copied, err := c.copyArchive(ctx, client, source, destination, enableAccessControl, logger)
	result.MeetingsMoved = copied.Copied
	result.MeetingsAlreadyThere = copied.AlreadyPresent
	result.CatalogMoved = copied.CatalogMerged
	if err != nil {
		return result, fmt.Errorf("%w — nothing was removed and Cassini is still in %s mode, so the recordings are all still in %s; fix the cause and switch again",
			err, storageModeName(current), source)
	}

	// 6. The flip. The archive is verified at the destination, so this is the
	//    instant the destination becomes the answer to "where are the
	//    recordings". Memory first, then disk: a running operator that kept
	//    believing the old mode would write the next recording into the tree
	//    that is about to be emptied.
	ncStorage.set(enableAccessControl, storageModeSourceConfigured, false)
	result.Mode = storageModeName(enableAccessControl)
	if err := c.recordStorageMode(enableAccessControl, storageModeSourceUser, false, logger); err != nil {
		// The flip is the settings write, so a write that failed is a flip that
		// did not happen. Nothing is lost: the copy is verified at the
		// destination and the source has not been touched, so BOTH roots hold a
		// complete archive and either mode is coherent.
		//
		// The preflight below re-reads the file and puts this process back in the
		// mode the file still names, which is the right resolution and the reason
		// this message must not claim otherwise — an earlier draft said Cassini
		// would keep using the new mode until it restarted, which the very next
		// line makes false.
		result.Mode = storageModeName(current)
		c.preflightNCStorageLocked(ctx, client, logger)
		return result, fmt.Errorf("every recording was copied into %s, but the new mode could not be saved: %w — Cassini is still in %s mode, where the recordings also still are, so nothing is lost and the switch can simply be asked for again once the volume is writable", destination, err, storageModeName(current))
	}

	// 7. Empty the source. Its collections stay: an empty `meetings` directory is
	//    what a re-opt-in writes into, and deleting a directory to recreate it a
	//    moment later is a chance to fail for nothing.
	if err := c.clearArchiveContents(ctx, client, source, logger); err != nil {
		result.LeftoverSource = source
		logger.Printf("nc storage: %s still holds a copy of the archive: %v", source, err)
		c.preflightNCStorageLocked(ctx, client, logger)
		return result, nil
	}
	result.SourceCleared = true

	// 8. Settled.
	if err := c.recordStorageMode(enableAccessControl, storageModeSourceUser, true, logger); err != nil {
		result.LeftoverSource = ""
		logger.Printf("nc storage: the switch finished but the settled flag could not be written: %v", err)
	} else {
		ncStorage.set(enableAccessControl, storageModeSourceConfigured, true)
	}

	logger.Printf("nc storage: switched to %s — %d recording(s) copied from %s into %s, source emptied",
		storageModeName(enableAccessControl), result.MeetingsMoved, source, destination)

	// Re-run the preflight in the same critical section so /status, /setup and
	// the publish gate describe the archive as it is NOW, rather than as it was
	// before the switch.
	c.preflightNCStorageLocked(ctx, client, logger)
	return result, nil
}

// recordStorageMode persists one step of the state machine and mirrors it into
// the process-wide record's clean flag.
//
// A missing settings path is not an error: an operator without a persistent
// volume still runs, it just cannot outlive its container. Every OTHER caller
// treats a failed write as fatal to the step it was part of, because the
// invariant this file rests on is a claim about what is written down.
func (c ExAppConfig) recordStorageMode(accessControlled bool, source string, clean bool, logger *log.Logger) error {
	path := ncStorage.settingsPath()
	if path == "" {
		ncStorage.set(accessControlled, storageModeSourceConfigured, clean)
		if logger != nil {
			logger.Printf("nc storage: no settings path configured; mode=%s clean=%t governs this process only", storageModeName(accessControlled), clean)
		}
		return nil
	}
	if err := SaveStorageSettings(path, accessControlled, source, clean); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	ncStorage.set(accessControlled, storageModeSourceConfigured, clean)
	return nil
}

// sanityForTarget asks whether a mode could be switched TO, which is a
// different question from whether the mode currently in force is usable.
//
// Since the split it is the same question in both directions — nothing about the
// current storage disqualifies the other model — but the two names are kept
// apart because the ASYMMETRY was load-bearing in the first pass and reuniting
// them would silently drop the distinction if it ever comes back.
func (p ncStorageProbe) sanityForTarget(accessControlled bool) (ok bool, step, detail string) {
	if accessControlled {
		return p.accessControlReady()
	}
	return p.defaultReady()
}

// archiveCopyResult is what one copy pass did.
type archiveCopyResult struct {
	Copied         int
	AlreadyPresent int
	CatalogMerged  bool
}

// copyArchive carries every meeting and the catalog from one root to another,
// leaving the source untouched.
//
// It is the engine behind all three journeys that relocate an archive: the
// opt-in, the opt-out, and the one-time adoption of a pre-split default tree.
// They differ in exactly one respect — whether the destination is inside the
// Team folder, which decides the ACL work — so that is the only parameter.
func (c ExAppConfig) copyArchive(ctx context.Context, client *http.Client, source, destination string, intoTeamFolder bool, logger *log.Logger) (archiveCopyResult, error) {
	var result archiveCopyResult

	// 2. The destination tree. Under access control the owner-only floor goes on
	//    the mount FIRST — the D-534/D-594 ordering: nothing may be reachable
	//    through a broad container grant before it states its own rules.
	if intoTeamFolder {
		if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, ncRecordingsMount, ownerOnlyContainerACLRules()); err != nil {
			return result, fmt.Errorf("apply the owner-only floor to %q: %w", ncRecordingsMount, err)
		}
	}
	if err := c.mkcolRecordingsTree(ctx, client, destination); err != nil {
		return result, err
	}

	// 3. The meetings.
	copied, already, err := c.copyMeetings(ctx, client, source+"/meetings", destination+"/meetings", intoTeamFolder, logger)
	result.Copied, result.AlreadyPresent = copied, already
	if err != nil {
		return result, err
	}

	// 4. The index.
	merged, err := c.mergeCatalogInto(ctx, client, source+"/catalog.json", destination+"/catalog.json", intoTeamFolder, logger)
	result.CatalogMerged = merged
	if err != nil {
		return result, err
	}

	// 5. Widen the container only now that every leaf under it states its own
	//    audience, then prove the copy is complete. The verification is what
	//    licenses the caller to flip the mode.
	if intoTeamFolder {
		if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, ncRecordingsMount, containerACLRules()); err != nil {
			return result, fmt.Errorf("grant %q read on %q: %w", ncRecordingsEveryoneGroup, ncRecordingsMount, err)
		}
	}
	if err := c.verifyArchiveCopied(ctx, client, source, destination); err != nil {
		return result, err
	}
	return result, nil
}

// copyMeetings copies every child of srcDir that is not already at dstDir.
//
// Skipping what is already there is what makes a re-run finish an interrupted
// copy rather than fail on it: `Overwrite` is never set, so a COPY onto an
// existing name answers 412, and treating that as an error would make the second
// attempt strictly worse than the first.
//
// intoTeamFolder decides the ACL work, and only one direction has any:
//
//	into the Team folder    PROPPATCH the public rule set onto the DESTINATION
//	                        leaf. The owner-only container floor is in force
//	                        while this runs, so nothing is readable in between.
//	out of the Team folder  nothing. A copy into the service account's own home
//	                        gets a new fileid outside any group folder, and
//	                        groupfolders keys its rules by fileid — so the copy
//	                        has no rules by construction. Writing one would fail:
//	                        `nc:acl-list` is not settable outside a Team folder
//	                        (500 with groupfolders installed, a false 207
//	                        without it — measured, D-616 spike x1).
func (c ExAppConfig) copyMeetings(ctx context.Context, client *http.Client, srcDir, dstDir string, intoTeamFolder bool, logger *log.Logger) (copied, alreadyPresent int, err error) {
	names, visible, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, srcDir)
	if err != nil {
		return 0, 0, fmt.Errorf("list %s: %w", srcDir, err)
	}
	if !visible {
		return 0, 0, nil
	}
	existing, _, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, dstDir)
	if err != nil {
		// Not swallowed. "We could not see what is already there" would make
		// every copy below a 412 and the whole switch a failure with no useful
		// message; worse, a later verification could pass against a tree we never
		// actually read.
		return 0, 0, fmt.Errorf("list %s: %w", dstDir, err)
	}
	present := make(map[string]bool, len(existing))
	for _, name := range existing {
		present[name] = true
	}

	for _, name := range names {
		src := srcDir + "/" + name
		dst := dstDir + "/" + name
		if present[name] {
			alreadyPresent++
			continue
		}
		if err := c.davCopy(ctx, client, ncRecordingsOwner, src, dst); err != nil {
			return copied, alreadyPresent, fmt.Errorf("copy %s to %s: %w", src, dst, err)
		}
		if intoTeamFolder {
			if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, dst, publicRecordingACLRules()); err != nil {
				return copied, alreadyPresent, fmt.Errorf("make %s readable after copying it: %w", dst, err)
			}
		}
		copied++
		if logger != nil {
			logger.Printf("nc storage: copied %s -> %s", src, dst)
		}
	}
	return copied, alreadyPresent, nil
}

// verifyArchiveCopied refuses to let the caller flip the mode until every
// recording at the source has a counterpart at the destination.
//
// This is the step that turns "the copies did not error" into "the destination
// is the archive". Without it the flip would rest on a loop's return value, and
// a source that grew during the copy — a publish that slipped in before the lock
// was taken, a name the listing missed — would be silently left behind at a root
// nothing reads any more.
func (c ExAppConfig) verifyArchiveCopied(ctx context.Context, client *http.Client, source, destination string) error {
	want, visible, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, source+"/meetings")
	if err != nil {
		return fmt.Errorf("verify %s: %w", source, err)
	}
	if !visible || len(want) == 0 {
		return nil
	}
	got, _, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, destination+"/meetings")
	if err != nil {
		return fmt.Errorf("verify %s: %w", destination, err)
	}
	present := make(map[string]bool, len(got))
	for _, name := range got {
		present[name] = true
	}
	var missing []string
	for _, name := range want {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%d of %d recording(s) did not reach %s (%s)", len(missing), len(want), destination, strings.Join(clip(missing, 3), ", "))
	}
	return nil
}

// clip shortens a list for an error message without hiding that it was longer.
func clip(items []string, max int) []string {
	if len(items) <= max {
		return items
	}
	return append(append([]string{}, items[:max]...), fmt.Sprintf("and %d more", len(items)-max))
}

// clearArchiveContents empties a recordings root without removing it.
//
// "Clear, do not delete" is deliberate, and it is not only tidiness: the empty
// collections are what the next migration in the other direction copies into,
// and an MKCOL that has already succeeded once is one fewer thing to fail. It
// also means an administrator looking at Files sees where the archive used to be
// rather than a hole.
func (c ExAppConfig) clearArchiveContents(ctx context.Context, client *http.Client, root string, logger *log.Logger) error {
	names, visible, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, root+"/meetings")
	if err != nil {
		return fmt.Errorf("list %s: %w", root+"/meetings", err)
	}
	if visible {
		for _, name := range names {
			rel := root + "/meetings/" + name
			if err := c.davDelete(ctx, client, ncRecordingsOwner, rel); err != nil {
				return fmt.Errorf("remove %s: %w", rel, err)
			}
			if logger != nil {
				logger.Printf("nc storage: removed %s", rel)
			}
		}
	}
	if err := c.davDelete(ctx, client, ncRecordingsOwner, root+"/catalog.json"); err != nil {
		return fmt.Errorf("remove %s: %w", root+"/catalog.json", err)
	}
	return nil
}

// finishMigration is the whole of the recovery: clear the root the recorded mode
// does NOT name, and record that the instance is settled.
//
// One action covers every way a migration can stop, because the invariant makes
// them the same shape. Whatever happened, `access_control_enabled` names a root
// holding a complete archive and the other root holds something nothing reads —
// a partial copy the switch never finished, or the original the tidy-up never
// removed. Discarding it is correct in both readings.
//
//	died before the flip   the partial copy at the target is discarded, the
//	                       instance stays in the mode it was in, and the switch
//	                       can simply be asked for again.
//	died after the flip    the original is removed, which is the step that did
//	                       not happen.
//
// It is idempotent, and a no-op on an install that is already clean — including
// every install that predates the flag, which reads as clean by absence.
func (c ExAppConfig) finishMigration(ctx context.Context, client *http.Client, logger *log.Logger) (storageTransitionResult, error) {
	accessControlled, resolved := ncStorage.mode()
	if !resolved {
		return storageTransitionResult{}, fmt.Errorf("Cassini has not resolved a storage mode yet, so there is nothing to finish")
	}
	stale := recordingsRootFor(!accessControlled)
	result := storageTransitionResult{
		Mode:            storageModeName(accessControlled),
		SourceRoot:      stale,
		DestinationRoot: recordingsRootFor(accessControlled),
	}
	if ncStorage.migrationClean() {
		result.SourceCleared = true
		return result, nil
	}

	// Never delete the only copy.
	//
	// The invariant says the recorded mode names a complete archive, so
	// everything at the stale root should already be at the active one — in both
	// readings of a failed migration. Proving it rather than trusting it costs one
	// PROPFIND pair and closes the one case where the invariant does NOT hold:
	// a pre-split archive that the enabled-edge adoption has not finished
	// carrying across, where the active root is genuinely the partial one. There
	// the honest answer is to refuse and say where the recordings are.
	if err := c.verifyArchiveCopied(ctx, client, stale, result.DestinationRoot); err != nil {
		result.LeftoverSource = stale
		return result, fmt.Errorf("refusing to clear %s: %w — those recordings are not in %s, so removing them would lose them. Switch storage mode again (or re-enable Cassini) to finish carrying them across first",
			stale, err, result.DestinationRoot)
	}

	if err := c.clearArchiveContents(ctx, client, stale, logger); err != nil {
		result.LeftoverSource = stale
		return result, fmt.Errorf("could not clear %s: %w — the recordings in %s are unaffected", stale, err, result.DestinationRoot)
	}
	result.SourceCleared = true
	if err := c.recordStorageMode(accessControlled, storageModeSourceUser, true, logger); err != nil {
		result.LeftoverSource = stale
		return result, fmt.Errorf("%s was cleared but the settled flag could not be written: %w", stale, err)
	}
	logger.Printf("nc storage: finished an unfinished migration — %s cleared, mode=%s is settled", stale, storageModeName(accessControlled))
	return result, nil
}

// adoptLegacyDefaultArchive carries a PRE-SPLIT default-mode archive into the
// default model's own root.
//
// Every install built by the first pass keeps its default-mode recordings at
// `Cassini/Recordings` — the path the Team folder also wants — or, if a Team
// folder was ever created, at whatever `Cassini (N)` the server renamed that
// tree to. Splitting the roots would stand those archives up in a place nothing
// reads any more, so the enabled edge carries them across.
//
// It deliberately does NOT use the migration_clean bookkeeping, and that is a
// safety property rather than a shortcut. A mode switch can flip which root is
// authoritative; an adoption cannot — the default model already reads
// `CassiniNoACL/Recordings`, so during an adoption the ACTIVE root is the
// incomplete one. Marking the instance dirty there would arm finishMigration
// against the very tree still holding the recordings. Instead the SOURCE is the
// state: copies skip what is already at the destination, so a re-run converges,
// and the source is emptied only once the copy is proven complete. An adoption
// that dies half way is finished by the next enabled edge, with nothing recorded
// and nothing at risk.
//
//	CassiniNoACL/Recordings has content  ─▶ still adopt if a legacy tree has any:
//	                                        the copy is by NAME, so a half-done
//	                                        adoption finishes rather than stalls
//	`Cassini` Team folder mounted        ─▶ never adopt from Cassini/Recordings.
//	                                        That is not a stranded default
//	                                        archive, it is the access-controlled
//	                                        model, and copying it into a private
//	                                        home tree would be a silent mode change
func (c ExAppConfig) adoptLegacyDefaultArchive(ctx context.Context, client *http.Client, probe ncStorageProbe, logger *log.Logger) {
	source, err := c.legacyDefaultArchiveRoot(ctx, client, probe)
	if err != nil {
		logger.Printf("nc storage: could not look for a pre-split recordings archive: %v", err)
		return
	}
	if source == "" {
		return
	}
	logger.Printf("nc storage: found a pre-split archive at %s; carrying it into %s", source, ncDefaultRecordingsRoot)
	copied, err := c.copyArchive(ctx, client, source, ncDefaultRecordingsRoot, false, logger)
	if err != nil {
		logger.Printf("ERROR: nc storage: could not carry %s into %s: %v — the recordings are still in %s and this will be retried on the next enable",
			source, ncDefaultRecordingsRoot, err, source)
		return
	}
	if err := c.clearArchiveContents(ctx, client, source, logger); err != nil {
		logger.Printf("nc storage: carried %d recording(s) from %s into %s, but %s could not be emptied: %v — it is a harmless duplicate and can be removed by hand",
			copied.Copied, source, ncDefaultRecordingsRoot, source, err)
		return
	}
	logger.Printf("nc storage: carried %d recording(s) from %s into %s and emptied it (%d were already there)",
		copied.Copied, source, ncDefaultRecordingsRoot, copied.AlreadyPresent)
}

// legacyDefaultArchiveRoot names a pre-split default archive that still holds
// recordings, or "" when there is none to carry.
func (c ExAppConfig) legacyDefaultArchiveRoot(ctx context.Context, client *http.Client, probe ncStorageProbe) (string, error) {
	// The canonical pre-split root, but only when nothing is mounted over it. A
	// mounted `Cassini` is the access-controlled model; a `Cassini` that is only a
	// directory is the old default one.
	if !probe.FolderMounted {
		if names, visible, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, ncLegacyDefaultRecordingsRoot+"/meetings"); err != nil {
			return "", fmt.Errorf("inspect %s: %w", ncLegacyDefaultRecordingsRoot, err)
		} else if visible && len(names) > 0 {
			return ncLegacyDefaultRecordingsRoot, nil
		}
	}
	// A tree the server renamed under an earlier collision, or a first-pass
	// opt-out that never finished. Both are private to the service account.
	stranded, err := c.findStrandedRecordingsRoot(ctx, client)
	if err != nil {
		return "", err
	}
	if stranded == "" {
		return "", nil
	}
	names, visible, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, stranded+"/meetings")
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", stranded, err)
	}
	if !visible || len(names) == 0 {
		return "", nil
	}
	return stranded, nil
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

// mergeCatalogInto copies the archive's index, merging rather than replacing
// when both sides have one. It leaves the source copy in place — removing it is
// the tidy-up's job, and doing it here would break the invariant that the source
// is only read until the mode has flipped.
//
// Replacing rather than merging would be the ordinary failure here and it is
// unrecoverable: the catalog is the only thing that makes a recording
// discoverable, upsert writes the merged document whole, and a later publish
// would append to whatever truncated file it found.
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
	if logger != nil {
		logger.Printf("nc storage: merged %s into %s", srcPath, dstPath)
	}
	return true, nil
}

// findStrandedRecordingsRoot looks for a PRE-SPLIT default-mode archive in the
// service account's home, and returns its recordings root ("" when there is
// none).
//
// This is legacy-only now. Nothing Cassini writes can be stranded any more — the
// two models have roots that cannot shadow each other — but an install from the
// first pass has its default-mode archive in one of three places, none of which
// the current default root is:
//
//	Cassini/Recordings        the pre-split default root, when no Team folder is
//	                          mounted over it. The caller decides that; this
//	                          function is not told about mounts.
//	Cassini (N)/Recordings    the same tree after a Team folder took the path and
//	                          the server renamed it. The suffix is server-chosen,
//	                          so it is matched as a pattern.
//	Cassini-optout/Recordings a first-pass opt-out that did not finish.
//
// The staging name is checked first because it is OURS, so finding it is
// unambiguous evidence about which transition left it.
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
		_, ok, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, root)
		if err != nil {
			// A failed look is not "there is no archive here". Swallowing it
			// would let an adoption complete having carried nothing, after which
			// the recordings stay in the stranded tree with nothing left to
			// notice them.
			return "", fmt.Errorf("inspect %s: %w", root, err)
		}
		if ok {
			return root, nil
		}
	}
	return "", nil
}

// davCopy copies relPath to destination within one account's Files.
//
// `Overwrite: F` is sent explicitly rather than left to the server default,
// because the default is TRUE: an omitted header on an existing destination
// silently destroys it, which for a directory means the whole tree (measured,
// D-660 part 2). Callers treat the resulting 412 as "already there" only when
// they have separately established that, which copyMeetings does by listing the
// destination first.
//
// `Depth: infinity` is what makes a legacy directory-shaped asset copy whole.
// It is the only legal value for a collection COPY in RFC 4918 and is ignored
// for a plain file.
func (c ExAppConfig) davCopy(ctx context.Context, client *http.Client, userID, relPath, destination string) error {
	req, err := http.NewRequestWithContext(ctx, "COPY", c.davFileURL(userID, relPath), nil)
	if err != nil {
		return err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Destination", c.davFileURL(userID, destination))
	req.Header.Set("Overwrite", "F")
	req.Header.Set("Depth", "infinity")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusPreconditionFailed {
		return fmt.Errorf("COPY %s -> %s: %s already exists (refusing to overwrite it)", relPath, destination, destination)
	}
	return fmt.Errorf("COPY %s -> %s: HTTP %d", relPath, destination, resp.StatusCode)
}

// davPropfindChildren lists the immediate children of relDir as userID and
// returns their basenames, excluding the collection itself.
//
// davPropfindNames answers the neighbouring question — which `.opus` files may
// this caller see — and filtering by extension is exactly wrong here: an archive
// may carry a legacy directory-shaped export, and a copy that skipped it would
// be verified as complete and then have its source deleted.
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

// storageTransitionPreview is what a mode switch WOULD do, computed without
// touching anything.
//
// The transition is the riskiest operation in the feature: it relocates an
// entire published archive and, going into the Team folder, makes every
// already-published recording readable by every account. Before this existed an
// administrator pressed a button and found out afterwards — the dialog stated
// the policy but none of the facts.
type storageTransitionPreview struct {
	// Mode is the mode being previewed, not the one in force.
	Mode string `json:"mode"`
	// Ready is sanityForTarget: whether the switch could run at all.
	Ready  bool   `json:"ready"`
	Step   string `json:"step,omitempty"`
	Detail string `json:"detail,omitempty"`

	SourceRoot      string `json:"source_root,omitempty"`
	DestinationRoot string `json:"destination_root,omitempty"`

	// SourceReadable says the source tree was actually listed.
	//
	// Without it a failed PROPFIND and an empty archive are the same zero, and
	// the dialog says "there are no published recordings to move" on the strength
	// of a question nobody managed to ask. That is the exact shape QA reported.
	SourceReadable bool `json:"source_readable"`

	// Meetings is how many would be copied. Zero with SourceReadable means the
	// tree really is empty.
	Meetings       int  `json:"meetings"`
	CatalogPresent bool `json:"catalog_present"`

	// DestinationMeetings is what is already at the destination. Non-zero is not
	// fatal — the copy merges and skips names it already finds — but it is the
	// single most important thing to say out loud before merging somebody's
	// archive.
	DestinationMeetings int `json:"destination_meetings"`

	// NothingToMove distinguishes "this is a no-op" from "this will move 41
	// meetings", which the confirmation copy has to say differently. It is false
	// whenever the source could not be read, because an unanswered question is
	// not a no-op.
	NothingToMove bool `json:"nothing_to_move"`

	// PendingCleanup is set when a previous migration did not finish, so the
	// administrator is told the stale root is cleared before this one starts.
	PendingCleanup string `json:"pending_cleanup,omitempty"`

	// Warnings are things an administrator should read before confirming.
	// Present tense, one sentence each, ordered most-surprising first.
	Warnings []string `json:"warnings,omitempty"`
}

// previewStorageModeSwitch answers what switchStorageMode would do. It issues
// PROPFINDs and nothing else — no MKCOL, no COPY, no PROPPATCH, no DELETE.
//
// Since the split there is nothing to discover: the source is the root of the
// mode in force and the destination is the root of the mode being asked about.
// The first pass asked `findStrandedRecordingsRoot` instead, which recognises a
// server-renamed tree and a staging directory and NOT the ordinary healthy
// archive — so on a normal default-mode install it answered "there is nothing
// here", the count was skipped, and the dialog said no recordings would move
// while the switch went on to move all of them.
func (c ExAppConfig) previewStorageModeSwitch(ctx context.Context, enableAccessControl bool, logger *log.Logger) (storageTransitionPreview, error) {
	if !c.appAPIActive() {
		return storageTransitionPreview{}, fmt.Errorf("storage mode can only be changed in a Nextcloud (AppAPI) deployment")
	}
	// The same lock the switch takes, so a preview cannot read a tree a
	// concurrent transition is half way through copying.
	provisionMu.Lock()
	defer provisionMu.Unlock()

	out := storageTransitionPreview{Mode: storageModeName(enableAccessControl)}
	client := &http.Client{Timeout: ncProvisionTimeout}
	probe, err := c.probeNCStorage(ctx, client, logger)
	if err != nil {
		return out, fmt.Errorf("could not inspect this Nextcloud: %w", err)
	}
	out.Ready, out.Step, out.Detail = probe.sanityForTarget(enableAccessControl)

	current, resolved := ncStorage.mode()
	if !resolved {
		return out, fmt.Errorf("Cassini has not resolved a storage mode yet, so it cannot say what a switch would move")
	}
	out.SourceRoot = recordingsRootFor(current)
	out.DestinationRoot = recordingsRootFor(enableAccessControl)
	if !ncStorage.migrationClean() {
		out.PendingCleanup = recordingsRootFor(!current)
	}

	out.Meetings, out.CatalogPresent, out.SourceReadable = c.countArchiveAt(ctx, client, out.SourceRoot)
	out.NothingToMove = out.SourceReadable && out.Meetings == 0 && !out.CatalogPresent
	out.DestinationMeetings, _, _ = c.countArchiveAt(ctx, client, out.DestinationRoot)

	out.Warnings = previewWarnings(out, enableAccessControl)
	return out, nil
}

// countArchiveAt reports how many meetings are under a recordings root, whether
// it carries a catalog, and whether the tree could be read at all.
//
// The third return is the point. It used to report zero for a tree that was
// absent, unreadable, or genuinely empty alike, and the confirmation dialog
// rendered all three as "nothing to move".
func (c ExAppConfig) countArchiveAt(ctx context.Context, client *http.Client, root string) (meetings int, catalog bool, readable bool) {
	names, visible, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, root+"/meetings")
	if err != nil {
		return 0, false, false
	}
	if visible {
		meetings = len(names)
	}
	siblings, siblingsVisible, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, root)
	if err != nil {
		return meetings, false, false
	}
	if siblingsVisible {
		for _, name := range siblings {
			if name == "catalog.json" {
				catalog = true
				break
			}
		}
	}
	// An absent root reads as an empty one, which it is: the migration creates
	// it. What must not read as empty is a root we could not ask about.
	return meetings, catalog, true
}

// previewWarnings is the copy an administrator reads before confirming. It says
// what is surprising, not what is normal — a preview that warns about everything
// is one nobody reads.
func previewWarnings(p storageTransitionPreview, enableAccessControl bool) []string {
	var out []string
	if !p.SourceReadable {
		out = append(out, fmt.Sprintf(
			"Cassini could not read %s, so it cannot say how many recordings would move. The switch checks again before it writes anything.", p.SourceRoot))
	}
	if p.PendingCleanup != "" {
		out = append(out, fmt.Sprintf(
			"An earlier switch did not finish. %s still holds a copy of the archive, and it is cleared before this switch starts.", p.PendingCleanup))
	}
	if p.DestinationMeetings > 0 {
		out = append(out, fmt.Sprintf(
			"%s already holds %d recording(s). They stay where they are, and any recording that is already there is not copied again.",
			p.DestinationRoot, p.DestinationMeetings))
	}
	if enableAccessControl && p.Meetings > 0 {
		out = append(out, fmt.Sprintf(
			"All %d copied recording(s) will be readable by every account. Cassini does not guess who was in a past meeting; narrow them afterwards from Files → Advanced permissions.", p.Meetings))
	}
	if !enableAccessControl && p.Meetings > 0 {
		out = append(out, fmt.Sprintf(
			"All %d recording(s) lose their access rules. After this, everyone who can open Cassini can read every one — including the ones restricted to a call's participants.", p.Meetings))
	}
	if !enableAccessControl {
		out = append(out, fmt.Sprintf(
			"The %q Team folder is emptied but left in place. Nothing is deleted from Nextcloud's folder list, and switching back later is immediate.", ncRecordingsMount))
	}
	return out
}
