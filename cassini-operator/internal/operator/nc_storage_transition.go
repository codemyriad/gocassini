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
	"time"
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
//
// The counters are per-outcome rather than one total because the policies differ
// in exactly what they do to each name, and a result that said "12 recordings
// were copied" about a switch that replaced four and left three behind would be
// describing a different operation from the one that ran (D-708).
type storageTransitionResult struct {
	Mode string `json:"mode"`
	// Strategy and OnConflict are what was actually applied, after defaulting —
	// so the UI reports the policy that ran rather than the one it asked for.
	Strategy   string `json:"strategy,omitempty"`
	OnConflict string `json:"on_conflict,omitempty"`
	// MeetingsMoved is everything that LANDED at the destination: copied plus
	// replaced. Kept under its first-pass name because it is the number an
	// administrator reads.
	MeetingsMoved   int    `json:"meetings_moved"`
	CatalogMoved    bool   `json:"catalog_moved"`
	SourceRoot      string `json:"source_root,omitempty"`
	DestinationRoot string `json:"destination_root,omitempty"`
	// MeetingsReplaced is how many of those overwrote a copy already at the
	// destination — `overwrite`, or `newest_wins` where the source was newer.
	MeetingsReplaced int `json:"meetings_replaced,omitempty"`
	// MeetingsSkipped is how many the destination kept.
	MeetingsSkipped int `json:"meetings_skipped,omitempty"`
	// MeetingsKeptInSource is how many kept a copy in the SOURCE as well, which
	// only the `skip` conflict policy produces. They are why the source is not
	// simply emptied, and why the recovery has to be told about them.
	MeetingsKeptInSource int `json:"meetings_kept_in_source,omitempty"`
	// MeetingsDeletedAtDestination is what `overwrite` removed because the
	// source did not have it. Nothing else ever makes this non-zero.
	MeetingsDeletedAtDestination int `json:"meetings_deleted_at_destination,omitempty"`
	// MeetingsAlreadyThere is how many of the source's recordings were already at
	// the destination and were therefore not copied again. Non-zero on a re-run
	// after a partial failure, which is the case worth naming out loud.
	MeetingsAlreadyThere int `json:"meetings_already_there,omitempty"`
	// SourceCleared says the source's contents were removed. False means the
	// archive arrived but the tidy-up did not finish, `migration_clean` is false,
	// and the Setup tab has a button for it. It is also false, legitimately, for
	// `switch_only` — nothing was carried, so nothing is cleared.
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
func (c ExAppConfig) switchStorageMode(ctx context.Context, enableAccessControl bool, requested storageMigrationPolicy, logger *log.Logger) (storageTransitionResult, error) {
	if !c.appAPIActive() {
		return storageTransitionResult{}, fmt.Errorf("storage mode can only be changed in a Nextcloud (AppAPI) deployment")
	}
	provisionMu.Lock()
	defer provisionMu.Unlock()

	// The mode is re-read HERE, under the lock, and this is the only reading of
	// it that decides anything.
	//
	// The handler has its own "already there" check, but it runs outside the
	// lock, which makes it a hint rather than a guard: two PUTs asking for the
	// same target both see the old mode, both pass it, the first one flips, and
	// the second arrives with current == target. Since recordingsRootFor is a
	// two-way switch, that makes source and destination the SAME root — and the
	// copy then trivially "succeeds" (every name is already at the destination),
	// the verification compares a listing with itself, and step 7 deletes the
	// archive it was supposed to be protecting. Reported as success.
	//
	// An unresolved record used to be the same trap by a different route: it read
	// as `default`, so a PUT asking for `default` got source == destination too,
	// and the first pass refused the whole operation there.
	//
	// Since D-708 an unresolved record is the ORDINARY starting state — nothing
	// falls back any more, so a fresh install has no mode until somebody picks
	// one, and this call is how they pick it. The trap is closed by naming the
	// source explicitly instead: with nothing recorded, the source is the root
	// the target does NOT name, which on an empty instance holds nothing and on
	// an upgraded one holds the archive the administrator is deciding about.
	current, resolved := ncStorage.mode()
	if resolved && current == enableAccessControl {
		// Already there. An unsettled instance still has a tidy-up to finish,
		// which is the request an administrator makes by pressing the button for
		// the mode already in force — see finishMigration.
		if ncStorage.migrationClean() {
			// The zero result IS the answer: nothing moved, so there is no
			// transition to report and the caller renders the current state
			// unchanged. A no-op that described a move would put "0 recordings
			// were copied" on screen every time somebody double-clicked.
			return storageTransitionResult{}, nil
		}
		return c.finishMigration(ctx, &http.Client{Timeout: ncProvisionTimeout}, logger)
	}

	client := &http.Client{Timeout: ncProvisionTimeout}
	probe, err := c.probeNCStorage(ctx, client, logger)
	if err != nil {
		return storageTransitionResult{}, fmt.Errorf("could not inspect this Nextcloud: %w", err)
	}
	if ready, step, detail := probe.sanityForTarget(enableAccessControl); !ready {
		return storageTransitionResult{}, fmt.Errorf("%w (%s): %s", errTransitionNotReady, step, detail)
	}

	policy, err := normalizeStorageMigrationPolicy(requested)
	if err != nil {
		return storageTransitionResult{}, fmt.Errorf("%w: %v", errStorageBadPolicy, err)
	}

	// The choice is re-taken HERE, under the lock, against the probe this switch
	// just ran — not against the one the preview showed.
	//
	// The preview takes the lock separately, so the conflicts an administrator
	// was shown are not necessarily the conflicts this switch will act on. A
	// request that DID name a policy is honoured either way: a policy for a
	// conflict that has since gone is a no-op, and refusing it would turn a race
	// into an error for no gain. A request that named none, on an instance that
	// now has a choice to make, is refused with the conflicts attached — so the
	// panel opens the controls the administrator was never shown, rather than a
	// default being picked on their behalf for an archive they have not seen.
	choice := storageChoiceFor(probe.archiveFor(!enableAccessControl), probe.archiveFor(enableAccessControl))
	if !requested.Asked() && choice.Required() {
		return storageTransitionResult{}, choiceRequiredError(choice, !enableAccessControl, enableAccessControl)
	}
	return c.migrateStorageLocked(ctx, client, resolved, enableAccessControl, policy, logger)
}

// errStorageBadPolicy is an unusable strategy or conflict rule. Nothing was
// touched; the request is wrong.
var errStorageBadPolicy = errors.New("that migration policy is not one Cassini knows")

// errStorageChoiceRequired means the two roots make the outcome depend on a
// decision the request did not carry. Nothing was touched.
var errStorageChoiceRequired = errors.New("this switch needs you to say what happens to the recordings that are already there")

func choiceRequiredError(choice storageMigrationChoice, from, to bool) error {
	var because []string
	if choice.StrategyMatters {
		because = append(because, fmt.Sprintf("there are recordings in both %s and %s", recordingsRootFor(from), recordingsRootFor(to)))
	}
	if choice.ConflictMatters {
		because = append(because, fmt.Sprintf("%d recording(s) exist under both (%s)", len(choice.Conflicts), strings.Join(clip(choice.Conflicts, 3), ", ")))
	}
	return fmt.Errorf("%w: %s", errStorageChoiceRequired, strings.Join(because, ", and "))
}

// migrateStorageLocked is the sequence itself, with provisionMu held and the
// target's prerequisites already confirmed.
//
// `recorded` says whether a mode was in force at all. It decides two things and
// nothing else: what the dirty mark names, and how the result describes where
// the archive was.
func (c ExAppConfig) migrateStorageLocked(ctx context.Context, client *http.Client, recorded, enableAccessControl bool, policy storageMigrationPolicy, logger *log.Logger) (storageTransitionResult, error) {
	// The source is the OTHER root, always.
	//
	// With a mode in force the caller has already established that it is not the
	// target, so "the other root" and "the recorded mode's root" are the same
	// thing. With nothing in force there is no recorded mode to ask, and the
	// other root is still where an archive would be — a pre-split install's
	// recordings, or the tree a previous attempt left behind. Deriving it this
	// way rather than from the mode is what makes source == destination
	// structurally impossible, which is the shape of the worst defect the first
	// pass shipped: a switch that migrated a root onto itself and then emptied it.
	source := recordingsRootFor(!enableAccessControl)
	destination := recordingsRootFor(enableAccessControl)

	// The mode the dirty mark names, and its provenance. NOT the user's choice:
	// they chose the target, and writing `user` here would confirm a decision in
	// the direction they are switching away from.
	originMode := !enableAccessControl
	originSource := storageModeSourceMigrating
	if recorded {
		originSource = ncStorage.recordedSource()
	}

	result := storageTransitionResult{
		Mode:            storageModeName(originMode),
		Strategy:        policy.Strategy,
		OnConflict:      policy.OnConflict,
		SourceRoot:      source,
		DestinationRoot: destination,
	}

	// `switch_only` is a settings write and nothing else.
	//
	// It does not go through the dirty/flip/clean sequence because it has nothing
	// to be interrupted between: no byte moves, so there is never a moment when
	// one root holds half an archive. What it DOES produce is an instance whose
	// recorded mode names a root the recordings are not in — which is exactly the
	// stranded-archive state /storage already reports and the Setup tab already
	// explains, and which switching back (or switching again with `merge`) ends.
	if !policy.copiesAnything() {
		if err := c.recordStorageMode(enableAccessControl, storageModeSourceUser, true, logger); err != nil {
			return result, fmt.Errorf("the storage mode could not be saved: %w — nothing was moved and Cassini is still in %s mode", err, storageModeName(originMode))
		}
		result.Mode = storageModeName(enableAccessControl)
		result.MeetingsKeptInSource = len(archiveNamesAt(ctx, c, client, source))
		logger.Printf("nc storage: switched to %s without moving anything; the recordings stay in %s",
			storageModeName(enableAccessControl), source)
		c.preflightNCStorageLocked(ctx, client, logger)
		return result, nil
	}

	// 1. Dirty BEFORE the first write, not after it. A process killed between the
	//    first MKCOL and this line would leave a directory nobody accounted for;
	//    killed after it, the leftovers are already claimed by the recovery.
	inFlight := &StorageMigrationRecord{Strategy: policy.Strategy, OnConflict: policy.OnConflict}
	if err := c.recordStorageModeWithMigration(originMode, originSource, false, inFlight, logger); err != nil {
		return result, fmt.Errorf("could not record that a storage migration is in progress, so nothing was moved: %w", err)
	}

	copied, err := c.copyArchive(ctx, client, source, destination, enableAccessControl, policy, logger)
	result.MeetingsMoved = len(copied.Plan.Copy) + len(copied.Plan.Replace)
	result.MeetingsReplaced = len(copied.Plan.Replace)
	result.MeetingsSkipped = len(copied.Plan.Skip)
	result.MeetingsKeptInSource = len(copied.Plan.KeepInSource)
	result.MeetingsDeletedAtDestination = len(copied.Plan.DeleteAtDestination)
	result.MeetingsAlreadyThere = len(copied.Plan.Skip)
	result.CatalogMoved = copied.CatalogMerged
	if err != nil {
		return result, fmt.Errorf("%w — nothing was removed and the recordings are all still in %s; fix the cause and switch again",
			err, source)
	}
	inFlight.KeepInSource = copied.Plan.KeepInSource

	// 6. The flip. The archive is verified at the destination, so this is the
	//    instant the destination becomes the answer to "where are the
	//    recordings". Memory first, then disk: a running operator that kept
	//    believing the old mode would write the next recording into the tree
	//    that is about to be emptied.
	ncStorage.set(enableAccessControl, storageModeSourceUser, false)
	result.Mode = storageModeName(enableAccessControl)
	if err := c.recordStorageModeWithMigration(enableAccessControl, storageModeSourceUser, false, inFlight, logger); err != nil {
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
		result.Mode = storageModeName(originMode)
		c.preflightNCStorageLocked(ctx, client, logger)
		return result, fmt.Errorf("every recording was copied into %s, but the new mode could not be saved: %w — Cassini is still in %s mode, where the recordings also still are, so nothing is lost and the switch can simply be asked for again once the volume is writable", destination, err, storageModeName(originMode))
	}

	// 7. Empty the source, EXCEPT what a skipped conflict deliberately kept
	//    there. Its collections stay: an empty `meetings` directory is what a
	//    re-opt-in writes into, and deleting a directory to recreate it a moment
	//    later is a chance to fail for nothing.
	if err := c.clearArchiveContents(ctx, client, source, copied.Plan.keepSet(), logger); err != nil {
		result.LeftoverSource = source
		logger.Printf("nc storage: %s still holds a copy of the archive: %v", source, err)
		c.preflightNCStorageLocked(ctx, client, logger)
		return result, nil
	}
	result.SourceCleared = true

	// 8. Settled. The in-flight record goes with it: a clean file must never
	//    describe a migration that is over.
	if err := c.recordStorageMode(enableAccessControl, storageModeSourceUser, true, logger); err != nil {
		result.LeftoverSource = ""
		logger.Printf("nc storage: the switch finished but the settled flag could not be written: %v", err)
	} else {
		ncStorage.set(enableAccessControl, storageModeSourceUser, true)
	}

	logger.Printf("nc storage: switched to %s (%s/%s) — %d carried from %s into %s (%d replaced, %d skipped, %d kept in the source, %d removed at the destination)",
		storageModeName(enableAccessControl), policy.Strategy, policy.OnConflict, result.MeetingsMoved, source, destination,
		result.MeetingsReplaced, result.MeetingsSkipped, result.MeetingsKeptInSource, result.MeetingsDeletedAtDestination)

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
	return c.recordStorageModeWithMigration(accessControlled, source, clean, nil, logger)
}

// recordStorageModeWithMigration is the same write, carrying the in-flight
// switch so the recovery knows which policy it is finishing and which names it
// must NOT delete.
func (c ExAppConfig) recordStorageModeWithMigration(accessControlled bool, source string, clean bool, migration *StorageMigrationRecord, logger *log.Logger) error {
	path := ncStorage.settingsPath()
	if path == "" {
		ncStorage.set(accessControlled, source, clean)
		if logger != nil {
			logger.Printf("nc storage: no settings path configured; mode=%s source=%s clean=%t governs this process only", storageModeName(accessControlled), source, clean)
		}
		return nil
	}
	if err := SaveStorageSettingsWithMigration(path, accessControlled, source, clean, migration); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// The source it just WROTE, not a stand-in for "read from disk". Flattening
	// it here is what made a fallback and an administrator's click read back the
	// same, so /storage could not say whether anybody had chosen (D-708).
	ncStorage.set(accessControlled, source, clean)
	return nil
}

// sanityForTarget asks whether a mode could be switched TO, which is a
// different question from whether the mode currently in force is usable.
//
// For the default mode it is now the SAME question, and that is a correction
// rather than a simplification. It used to be deliberately weaker —
// defaultReady() alone — because the first pass's opt-out was the very operation
// that cleared the mounted Team folder its sanity check refused. Nothing clears
// anything now, so the weaker form had no job left; what it did instead was let
// an opt-out copy an entire access-controlled archive into a root that a Team
// folder was mounted over, which is exactly the disclosure sanity(default)
// exists to prevent, performed deliberately and at scale.
func (p ncStorageProbe) sanityForTarget(accessControlled bool) (ok bool, step, detail string) {
	if accessControlled {
		return p.accessControlReady()
	}
	return p.sanity(false)
}

// archiveCopyResult is what one copy pass did.
type archiveCopyResult struct {
	// Plan is what was decided before anything was written. The preview computes
	// exactly this and renders the counts, so the numbers an administrator
	// confirms come from the code that acts on them.
	Plan          archiveCarryPlan
	CatalogMerged bool
}

// copyArchive carries every meeting and the catalog from one root to another,
// leaving the source untouched.
//
// It is the engine behind all three journeys that relocate an archive: the
// opt-in, the opt-out, and the one-time adoption of a pre-split default tree.
// They differ in exactly one respect — whether the destination is inside the
// Team folder, which decides the ACL work — so that is the only parameter.
func (c ExAppConfig) copyArchive(ctx context.Context, client *http.Client, source, destination string, intoTeamFolder bool, policy storageMigrationPolicy, logger *log.Logger) (archiveCopyResult, error) {
	var result archiveCopyResult

	// The plan comes first, from one listing of each side, and every write below
	// follows it. Deciding per name inside the copy loop would mean the counts
	// were a by-product of the loop rather than the thing it executes — and the
	// preview could then only guess at them.
	sourceFacts, err := c.archiveEntriesAt(ctx, client, source)
	if err != nil {
		return result, err
	}
	destinationFacts, err := c.archiveEntriesAt(ctx, client, destination)
	if err != nil {
		// Not swallowed. "We could not see what is already there" would make
		// every copy below a 412 and the whole switch a failure with no useful
		// message; worse, a later verification could pass against a tree we never
		// actually read.
		return result, err
	}
	result.Plan = planArchiveCarry(policy, sourceFacts, destinationFacts)

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

	// 2b. Overwrite, and only overwrite, empties the destination of what the
	//     source does not have. This is the one thing a switch does that the
	//     administrator did not ask to MOVE, which is why it is counted and
	//     stated separately everywhere it appears.
	for _, name := range result.Plan.DeleteAtDestination {
		rel := destination + "/meetings/" + name
		if err := c.davDelete(ctx, client, ncRecordingsOwner, rel); err != nil {
			return result, fmt.Errorf("remove %s: %w", rel, err)
		}
		if logger != nil {
			logger.Printf("nc storage: removed %s (overwrite)", rel)
		}
	}

	// 3. The meetings.
	if err := c.carryMeetings(ctx, client, source+"/meetings", destination+"/meetings", intoTeamFolder, result.Plan, logger); err != nil {
		return result, err
	}

	// 4. The index.
	merged, err := c.mergeCatalogInto(ctx, client, source+"/catalog.json", destination+"/catalog.json", intoTeamFolder, policy, result.Plan, logger)
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

// archiveEntriesAt lists one root's meetings collection with timestamps, as the
// facts the plan is computed from.
func (c ExAppConfig) archiveEntriesAt(ctx context.Context, client *http.Client, root string) (ncArchiveFacts, error) {
	facts := ncArchiveFacts{Root: root}
	entries, visible, err := c.davPropfindEntries(ctx, client, ncRecordingsOwner, root+"/meetings")
	if err != nil {
		return facts, fmt.Errorf("list %s: %w", root+"/meetings", err)
	}
	facts.Probed = true
	facts.Present = visible
	facts.Entries = entries
	return facts, nil
}

// archiveNamesAt lists a root's recordings for a report, and answers an empty
// list rather than an error — it is used where the count is decoration on an
// operation that has already succeeded.
func archiveNamesAt(ctx context.Context, c ExAppConfig, client *http.Client, root string) []string {
	names, _, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, root+"/meetings")
	if err != nil {
		return nil
	}
	return names
}

// carryMeetings executes a plan: copy what the destination does not have,
// replace what the policy says the source wins, and leave the rest.
//
// Skipping what is already there is what makes a re-run finish an interrupted
// copy rather than fail on it: `Overwrite` is never set, so a COPY onto an
// existing name answers 412, and treating that as an error would make the second
// attempt strictly worse than the first.
//
// A REPLACE is delete-then-copy, and never `Overwrite: T`. Measured, D-660
// part 2: `Overwrite: T` destroys the destination's fileid — and with it every
// groupfolders ACL row keyed on that fileid — for a file, and for a collection
// it deletes the whole destination tree first. Stating the intent as two
// requests is the same operation with none of that delegated to a header whose
// default is data loss.
//
// intoTeamFolder decides the ACL work, and only one direction has any:
//
//	into the Team folder    PROPPATCH the public rule set onto the DESTINATION
//	                        leaf. The owner-only container floor is in force
//	                        while this runs, so nothing is readable in between.
//	                        It is applied to a REPLACED leaf too, and that is not
//	                        belt and braces: the replacement is a new file with a
//	                        new fileid, so it has no rules at all until this runs.
//	out of the Team folder  nothing. A copy into the service account's own home
//	                        gets a new fileid outside any group folder, and
//	                        groupfolders keys its rules by fileid — so the copy
//	                        has no rules by construction. Writing one would fail:
//	                        `nc:acl-list` is not settable outside a Team folder
//	                        (500 with groupfolders installed, a false 207
//	                        without it — measured, D-616 spike x1).
func (c ExAppConfig) carryMeetings(ctx context.Context, client *http.Client, srcDir, dstDir string, intoTeamFolder bool, plan archiveCarryPlan, logger *log.Logger) error {
	carry := func(name string, replacing bool) error {
		src := srcDir + "/" + name
		dst := dstDir + "/" + name
		if replacing {
			if err := c.davDelete(ctx, client, ncRecordingsOwner, dst); err != nil {
				return fmt.Errorf("remove %s before replacing it: %w", dst, err)
			}
		}
		if err := c.davCopy(ctx, client, ncRecordingsOwner, src, dst); err != nil {
			return fmt.Errorf("copy %s to %s: %w", src, dst, err)
		}
		if intoTeamFolder {
			if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, dst, publicRecordingACLRules()); err != nil {
				return fmt.Errorf("make %s readable after copying it: %w", dst, err)
			}
		}
		if logger != nil {
			if replacing {
				logger.Printf("nc storage: replaced %s with %s", dst, src)
			} else {
				logger.Printf("nc storage: copied %s -> %s", src, dst)
			}
		}
		return nil
	}
	for _, name := range plan.Copy {
		if err := carry(name, false); err != nil {
			return err
		}
	}
	for _, name := range plan.Replace {
		if err := carry(name, true); err != nil {
			return err
		}
	}
	return nil
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

// clearArchiveContents empties a recordings root without removing it, except for
// whatever `keep` names.
//
// "Clear, do not delete" is deliberate, and it is not only tidiness: the empty
// collections are what the next migration in the other direction copies into,
// and an MKCOL that has already succeeded once is one fewer thing to fail. It
// also means an administrator looking at Files sees where the archive used to be
// rather than a hole.
//
// `keep` is the `skip` conflict policy made real. Those recordings exist under
// both roots on purpose — the destination's copy was chosen, and the source's
// was NOT thrown away — so the tidy-up steps over them, and the catalog is
// rewritten to describe exactly what is left rather than deleted outright. Every
// other policy converges on one copy and passes nil.
func (c ExAppConfig) clearArchiveContents(ctx context.Context, client *http.Client, root string, keep map[string]bool, logger *log.Logger) error {
	names, visible, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, root+"/meetings")
	if err != nil {
		return fmt.Errorf("list %s: %w", root+"/meetings", err)
	}
	if visible {
		for _, name := range names {
			if keep[name] {
				continue
			}
			rel := root + "/meetings/" + name
			if err := c.davDelete(ctx, client, ncRecordingsOwner, rel); err != nil {
				return fmt.Errorf("remove %s: %w", rel, err)
			}
			if logger != nil {
				logger.Printf("nc storage: removed %s", rel)
			}
		}
	}
	if len(keep) == 0 {
		if err := c.davDelete(ctx, client, ncRecordingsOwner, root+"/catalog.json"); err != nil {
			return fmt.Errorf("remove %s: %w", root+"/catalog.json", err)
		}
		return nil
	}
	// Something stayed, so the index has to describe what stayed. Left whole it
	// would list recordings this root no longer has, and a viewer pointed at it
	// would render entries whose audio is gone.
	return c.restrictCatalogTo(ctx, client, root+"/"+ncSiteCatalogName, keep, logger)
}

// restrictCatalogTo rewrites a catalog down to the entries whose recordings are
// still under this root.
func (c ExAppConfig) restrictCatalogTo(ctx context.Context, client *http.Client, path string, keep map[string]bool, logger *log.Logger) error {
	raw, status, err := c.davGetBytes(ctx, client, ncRecordingsOwner, path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if status == http.StatusNotFound {
		return nil
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("read %s -> HTTP %d", path, status)
	}
	var catalog siteCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	keepIDs := make(map[string]bool, len(keep))
	for name := range keep {
		keepIDs[catalogIDFor(name)] = true
	}
	kept := make([]json.RawMessage, 0, len(keepIDs))
	for _, entry := range catalog.Meetings {
		id, err := catalogEntryID(entry)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if keepIDs[id] {
			kept = append(kept, entry)
		}
	}
	catalog.Meetings = kept
	body, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	body = append(body, '\n')
	if err := c.davPutBytes(ctx, client, ncRecordingsOwner, path, body, "application/json"); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if logger != nil {
		logger.Printf("nc storage: %s now lists the %d recording(s) that stayed", path, len(kept))
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

	// What the interrupted switch asked to KEEP at the stale root.
	//
	// Without this the recovery would delete exactly the copies an administrator
	// chose to preserve: `skip` leaves the source's version of a conflicting
	// recording where it is, on purpose, and "clear the root the recorded mode
	// does not name" would take them. The list is written at the flip, which is
	// the first instant it is known and the last instant before anything is
	// removed.
	var keep map[string]bool
	if path := ncStorage.settingsPath(); path != "" {
		settings, err := LoadStorageSettings(path)
		if err != nil {
			return result, fmt.Errorf("could not read %s to find out what the interrupted switch was doing: %w", storageSettingsFileName, err)
		}
		if settings.Migration != nil {
			keep = nameSet(settings.Migration.KeepInSource)
			result.Strategy = settings.Migration.Strategy
			result.OnConflict = settings.Migration.OnConflict
			result.MeetingsKeptInSource = len(settings.Migration.KeepInSource)
		}
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

	if err := c.clearArchiveContents(ctx, client, stale, keep, logger); err != nil {
		result.LeftoverSource = stale
		return result, fmt.Errorf("could not clear %s: %w — the recordings in %s are unaffected", stale, err, result.DestinationRoot)
	}
	result.SourceCleared = true
	// The provenance is PRESERVED, not promoted. Finishing a switch is a
	// tidy-up, and an instance that reached this state without anybody choosing
	// a mode — an interrupted first decision — must still be asked.
	source := ncStorage.recordedSource()
	if source == "" {
		source = storageModeSourceMigrating
	}
	if err := c.recordStorageMode(accessControlled, source, true, logger); err != nil {
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
	// The adoption is always a plain merge that skips what is already at the
	// destination: it is finishing a move nobody chose a policy for, and the
	// destination's copy is by definition the one an earlier run put there.
	copied, err := c.copyArchive(ctx, client, source, ncDefaultRecordingsRoot, false, defaultStorageMigrationPolicy(), logger)
	if err != nil {
		logger.Printf("ERROR: nc storage: could not carry %s into %s: %v — the recordings are still in %s and this will be retried on the next enable",
			source, ncDefaultRecordingsRoot, err, source)
		return
	}
	if err := c.clearArchiveContents(ctx, client, source, nil, logger); err != nil {
		logger.Printf("nc storage: carried %d recording(s) from %s into %s, but %s could not be emptied: %v — it is a harmless duplicate and can be removed by hand",
			len(copied.Plan.Copy), source, ncDefaultRecordingsRoot, source, err)
		return
	}
	logger.Printf("nc storage: carried %d recording(s) from %s into %s and emptied it (%d were already there)",
		len(copied.Plan.Copy), source, ncDefaultRecordingsRoot, len(copied.Plan.Skip))
}

// legacyDefaultArchiveRoot names a pre-split default archive that still holds
// recordings, or "" when there is none to carry.
func (c ExAppConfig) legacyDefaultArchiveRoot(ctx context.Context, client *http.Client, probe ncStorageProbe) (string, error) {
	// The canonical pre-split root, but only when nothing is mounted over it AND
	// somebody actually looked. A mounted `Cassini` is the access-controlled
	// model; a `Cassini` that is only a directory is the old default one; and a
	// `Cassini` nobody could ask about is neither.
	//
	// The distinction is the whole safety of this branch. FolderMounted is false
	// in both of the last two cases, and treating an unanswered question as "no
	// Team folder" would take a LIVE access-controlled archive, copy it into the
	// tree the operator serves to every caller as its owner, and then empty the
	// Team folder it came from. One transient OCS failure on the enabled edge is
	// enough. The other two candidates below need no such check: nothing ever
	// mounts a group folder at `Cassini (N)` or `Cassini-optout`.
	if probe.FolderProbed && !probe.FolderMounted {
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

// mergeCatalogInto copies the archive's index, applying the same policy the
// recordings got.
//
// The catalog is not a file among files: it is the only thing that makes a
// recording discoverable, upsert writes the merged document whole, and a later
// publish appends to whatever it finds. So each policy needs its own rule rather
// than inheriting one:
//
//	merge      upsert, MINUS the ids whose recording the destination kept. Those
//	           entries describe the destination's file, not the source's, and
//	           letting the source's entry win would point the index at a
//	           recording that is not the one under that name.
//	overwrite  replace. The destination is being made into the source, and an
//	           entry for a recording overwrite has just deleted is an index that
//	           renders a meeting with no audio.
//	switch_only never reaches here — nothing moved, so neither index changes.
//
// It leaves the source copy in place: removing it is the tidy-up's job, and doing
// it here would break the invariant that the source is only read until the mode
// has flipped.
func (c ExAppConfig) mergeCatalogInto(ctx context.Context, client *http.Client, srcPath, dstPath string, intoTeamFolder bool, policy storageMigrationPolicy, plan archiveCarryPlan, logger *log.Logger) (bool, error) {
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
	// The entries whose recording did NOT come from the source are dropped
	// before the merge, which is how "the destination won this name" is
	// expressed — upsertSiteCatalog replaces a matching id in place, so ordering
	// cannot say it.
	if !policy.clearsDestinationFirst() && len(plan.Skip) > 0 {
		held := make(map[string]bool, len(plan.Skip))
		for _, name := range plan.Skip {
			held[catalogIDFor(name)] = true
		}
		kept := make([]json.RawMessage, 0, len(source.Meetings))
		for _, entry := range source.Meetings {
			id, err := catalogEntryID(entry)
			if err != nil {
				return false, fmt.Errorf("%s: %w", srcPath, err)
			}
			if !held[id] {
				kept = append(kept, entry)
			}
		}
		source.Meetings = kept
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

	merged := source
	if !policy.clearsDestinationFirst() {
		var err error
		merged, err = upsertSiteCatalog(destination, source, catalogEntryOverlay{})
		if err != nil {
			return false, fmt.Errorf("merge %s into %s: %w", srcPath, dstPath, err)
		}
	} else if strings.TrimSpace(merged.Version) == "" {
		// Overwrite replaces the document, so the destination's version string
		// is the only thing worth carrying forward from it.
		merged.Version = destination.Version
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

// davEntry is one child of a collection: its basename and when it was last
// written.
//
// The timestamp is what the `newest_wins` conflict policy compares (D-708). It
// is best-effort — an entry whose `getlastmodified` is absent or unparseable
// carries the zero time, and every caller treats that as "cannot say", never as
// "very old". Deciding which of two recordings to keep on the strength of a date
// nobody could read is exactly the shape of mistake this feature keeps
// eliminating.
type davEntry struct {
	Name     string
	Modified time.Time
}

// davPropfindChildren lists the immediate children of relDir as userID and
// returns their basenames, excluding the collection itself.
//
// davPropfindNames answers the neighbouring question — which `.opus` files may
// this caller see — and filtering by extension is exactly wrong here: an archive
// may carry a legacy directory-shaped export, and a copy that skipped it would
// be verified as complete and then have its source deleted.
func (c ExAppConfig) davPropfindChildren(ctx context.Context, client *http.Client, userID, relDir string) (names []string, visible bool, err error) {
	entries, visible, err := c.davPropfindEntries(ctx, client, userID, relDir)
	if err != nil || !visible {
		return nil, visible, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name)
	}
	return out, true, nil
}

// davPropfindEntries is davPropfindChildren with the modification times, in the
// same single request.
//
// `getlastmodified` rides along with `resourcetype` rather than in a second
// PROPFIND because the two answers must describe the same instant: a conflict
// policy that decided which side is newer from one listing and which names exist
// from another could act on a pair that never coexisted.
func (c ExAppConfig) davPropfindEntries(ctx context.Context, client *http.Client, userID, relDir string) (entries []davEntry, visible bool, err error) {
	reqBody := []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/><d:getlastmodified/></d:prop></d:propfind>`)
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
			Href     string `xml:"href"`
			Propstat []struct {
				Status string `xml:"status"`
				Prop   struct {
					LastModified string `xml:"getlastmodified"`
				} `xml:"prop"`
			} `xml:"propstat"`
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
	out := make([]davEntry, 0, len(ms.Responses))
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
		entry := davEntry{Name: base}
		for _, ps := range r.Propstat {
			raw := strings.TrimSpace(ps.Prop.LastModified)
			if raw == "" {
				continue
			}
			// RFC 1123 with a numeric zone is what Nextcloud sends; the `GMT`
			// spelling is what RFC 4918 requires. Try both and give up quietly:
			// a date nobody can parse must not become a very old one.
			if when, perr := http.ParseTime(raw); perr == nil {
				entry.Modified = when
				break
			}
		}
		out = append(out, entry)
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
	// DestinationReadable is the same distinction SourceReadable draws, for the
	// other tree. Without it a destination nobody could list looks empty, and an
	// empty destination is the one shape where no policy question arises.
	DestinationReadable bool `json:"destination_readable"`

	// Strategy and OnConflict are the policy these numbers describe, after
	// defaulting — so the panel can label a diff it asked for with the policy
	// that actually produced it.
	Strategy   string `json:"strategy"`
	OnConflict string `json:"on_conflict"`

	// ChoiceRequired says the outcome depends on a decision. It is the ONLY
	// thing the migration controls are shown for: the spec's rule is that a
	// choice with one possible answer is not a choice, and asking anyway is how
	// a confirmation dialog stops being read.
	//
	// StrategyMatters is recordings in both roots; ConflictMatters is the SAME
	// recording in both, which is the only thing `on_conflict` can act on.
	ChoiceRequired  bool `json:"choice_required"`
	StrategyMatters bool `json:"strategy_matters"`
	ConflictMatters bool `json:"conflict_matters"`
	// ConflictNames are the names in both roots, clipped for display.
	ConflictNames []string `json:"conflict_names,omitempty"`
	Conflicts     int      `json:"conflicts"`

	// What THIS policy would do, per outcome. Computed by the same function the
	// switch executes, so the numbers on screen are not a second implementation
	// of the rules.
	WouldCopy                int `json:"would_copy"`
	WouldReplace             int `json:"would_replace"`
	WouldSkip                int `json:"would_skip"`
	WouldKeepInSource        int `json:"would_keep_in_source"`
	WouldDeleteAtDestination int `json:"would_delete_at_destination"`

	// NothingToMove distinguishes "this is a no-op" from "this will move 41
	// meetings", which the confirmation copy has to say differently. It is false
	// whenever the source could not be read, because an unanswered question is
	// not a no-op.
	NothingToMove bool `json:"nothing_to_move"`

	// PendingCleanup is set when a previous migration did not finish, so the
	// administrator knows there is a leftover copy somewhere before they start
	// another switch. The switch does NOT clear it first — it merges into its
	// destination and skips names already present — because forcing a cleanup
	// first turned a correctly-refused cleanup into a switch that could never
	// run.
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
func (c ExAppConfig) previewStorageModeSwitch(ctx context.Context, enableAccessControl bool, requested storageMigrationPolicy, logger *log.Logger) (storageTransitionPreview, error) {
	if !c.appAPIActive() {
		return storageTransitionPreview{}, fmt.Errorf("storage mode can only be changed in a Nextcloud (AppAPI) deployment")
	}
	// The same lock the switch takes, so a preview cannot read a tree a
	// concurrent transition is half way through copying.
	provisionMu.Lock()
	defer provisionMu.Unlock()

	out := storageTransitionPreview{Mode: storageModeName(enableAccessControl)}
	policy, err := normalizeStorageMigrationPolicy(requested)
	if err != nil {
		return out, fmt.Errorf("%w: %v", errStorageBadPolicy, err)
	}
	out.Strategy, out.OnConflict = policy.Strategy, policy.OnConflict

	client := &http.Client{Timeout: ncProvisionTimeout}
	probe, err := c.probeNCStorage(ctx, client, logger)
	if err != nil {
		return out, fmt.Errorf("could not inspect this Nextcloud: %w", err)
	}
	out.Ready, out.Step, out.Detail = probe.sanityForTarget(enableAccessControl)

	// The source is the OTHER root, exactly as the switch derives it — including
	// on an install that has not chosen a mode yet, which is the state the setup
	// wizard previews from.
	current, resolved := ncStorage.mode()
	out.SourceRoot = recordingsRootFor(!enableAccessControl)
	out.DestinationRoot = recordingsRootFor(enableAccessControl)
	if resolved && !ncStorage.migrationClean() {
		out.PendingCleanup = recordingsRootFor(!current)
	}

	sourceFacts := probe.archiveFor(!enableAccessControl)
	destinationFacts := probe.archiveFor(enableAccessControl)
	out.SourceReadable = sourceFacts.Probed
	out.Meetings = sourceFacts.Meetings()
	out.CatalogPresent = sourceFacts.Catalog
	out.DestinationReadable = destinationFacts.Probed
	out.DestinationMeetings = destinationFacts.Meetings()
	out.NothingToMove = out.SourceReadable && out.Meetings == 0 && !out.CatalogPresent

	choice := storageChoiceFor(sourceFacts, destinationFacts)
	out.ChoiceRequired = choice.Required()
	out.StrategyMatters = choice.StrategyMatters
	out.ConflictMatters = choice.ConflictMatters
	out.Conflicts = len(choice.Conflicts)
	out.ConflictNames = clip(choice.Conflicts, 20)

	// The plan, from the function the switch executes. A second implementation
	// of the rules here is exactly how a confirmation dialog comes to promise
	// something the operation does not do — which is the class of bug the
	// preview was added to fix in the first place.
	plan := planArchiveCarry(policy, sourceFacts, destinationFacts)
	out.WouldCopy = len(plan.Copy)
	out.WouldReplace = len(plan.Replace)
	out.WouldSkip = len(plan.Skip)
	out.WouldKeepInSource = len(plan.KeepInSource)
	out.WouldDeleteAtDestination = len(plan.DeleteAtDestination)

	out.Warnings = previewWarnings(out, enableAccessControl, policy)
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
func previewWarnings(p storageTransitionPreview, enableAccessControl bool, policy storageMigrationPolicy) []string {
	var out []string
	if !p.SourceReadable {
		out = append(out, fmt.Sprintf(
			"Cassini could not read %s, so it cannot say how many recordings would move. The switch checks again before it writes anything.", p.SourceRoot))
	}
	if !p.DestinationReadable {
		out = append(out, fmt.Sprintf(
			"Cassini could not read %s, so it cannot say what is already there. The switch checks again before it writes anything.", p.DestinationRoot))
	}
	if p.PendingCleanup != "" {
		out = append(out, fmt.Sprintf(
			"An earlier switch did not finish, so %s still holds a copy of the archive. This switch merges rather than duplicating, and \"Finish the switch\" clears the leftovers afterwards.", p.PendingCleanup))
	}

	// The single most consequential sentence, and the only one that describes a
	// DELETION the administrator did not ask to move.
	if p.WouldDeleteAtDestination > 0 {
		out = append(out, fmt.Sprintf(
			"%d recording(s) in %s are not in %s and will be DELETED, because \"overwrite\" makes the destination match the source exactly. Choose \"merge\" to keep them.",
			p.WouldDeleteAtDestination, p.DestinationRoot, p.SourceRoot))
	}
	if p.WouldReplace > 0 {
		out = append(out, fmt.Sprintf(
			"%d recording(s) exist in both, and %s's copy replaces the one in %s.", p.WouldReplace, p.SourceRoot, p.DestinationRoot))
	}
	if p.WouldSkip > 0 && p.WouldKeepInSource > 0 {
		out = append(out, fmt.Sprintf(
			"%d recording(s) exist in both. %s keeps its copy, and %s keeps its own — so both survive and %s is not fully emptied.",
			p.WouldSkip, p.DestinationRoot, p.SourceRoot, p.SourceRoot))
	} else if p.WouldSkip > 0 {
		out = append(out, fmt.Sprintf(
			"%d recording(s) exist in both. %s keeps its copy and the one in %s is removed with the rest.",
			p.WouldSkip, p.DestinationRoot, p.SourceRoot))
	}
	if !policy.copiesAnything() && p.Meetings > 0 {
		out = append(out, fmt.Sprintf(
			"Nothing is copied. All %d recording(s) stay in %s, which the %s mode does not read, so they will not be listed until you switch back or migrate them.",
			p.Meetings, p.SourceRoot, storageModeName(enableAccessControl)))
	} else if p.DestinationMeetings > 0 && p.WouldDeleteAtDestination == 0 {
		out = append(out, fmt.Sprintf(
			"%s already holds %d recording(s). They stay where they are.", p.DestinationRoot, p.DestinationMeetings))
	}

	carried := p.WouldCopy + p.WouldReplace
	if enableAccessControl && carried > 0 {
		out = append(out, fmt.Sprintf(
			"All %d copied recording(s) will be readable by every account. Cassini does not guess who was in a past meeting; narrow them afterwards from Files → Advanced permissions.", carried))
	}
	if !enableAccessControl && carried > 0 {
		out = append(out, fmt.Sprintf(
			"All %d copied recording(s) lose their access rules. After this, everyone who can open Cassini can read every one — including the ones restricted to a call's participants.", carried))
	}
	if !enableAccessControl {
		out = append(out, fmt.Sprintf(
			"The %q Team folder is left in place. Nothing is deleted from Nextcloud's folder list, and switching back later is immediate.", ncRecordingsMount))
	}
	return out
}
