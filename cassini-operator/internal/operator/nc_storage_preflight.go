package operator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
)

// The AppAPI enabled edge, after the opt-in (D-616 first pass).
//
// What used to run here was a provisioner: it created the service account, the
// groups, the Team folder and the ACL topology, and reported which step failed.
// That was the right shape when there was one storage model and the app's job
// was to build it. With two models the app's job changes — it has to find out
// which model this instance is set up for before it may touch anything, and the
// prerequisites of either model are the administrator's to install.
//
//	enabled edge
//	  │
//	  ├── probe            read only: apps, account, groups, folder, tree
//	  ├── resolve mode     the flag, or a default derived from the probe
//	  │                    (derived once, then persisted — never re-derived)
//	  ├── sanity check     does the storage match the mode it claims?
//	  ├── arrange          inside the storage the administrator provided:
//	  │                    the canonical collections, and in access-controlled
//	  │                    mode the container ACL + leaf self-heal
//	  └── record           ncAccessSubstrate → /status, /setup, /storage
//
// The one thing it will not do is create a prerequisite. A missing app, a
// missing account, an absent Team folder are reported with the command that
// fixes them and nothing else happens — which is also why a bad setup can no
// longer half-build a substrate that later reads as healthy.

// preflightNCStorage runs the enabled-edge preflight. No-op outside AppAPI.
// Non-fatal in every branch: an operator that cannot reach Nextcloud should
// still come up so an administrator can look at it.
func (c ExAppConfig) preflightNCStorage(ctx context.Context, logger *log.Logger) {
	if !c.appAPIActive() {
		return
	}
	provisionMu.Lock()
	defer provisionMu.Unlock()
	client := &http.Client{Timeout: ncProvisionTimeout}
	c.preflightNCStorageLocked(ctx, client, logger)
}

// preflightNCStorageLocked is the body, with provisionMu already held. The
// transition calls it directly to refresh the record after a mode change,
// inside the same critical section that performed the change — so no publish
// can observe the moved archive under the old mode.
func (c ExAppConfig) preflightNCStorageLocked(ctx context.Context, client *http.Client, logger *log.Logger) {
	// This run's verdict is this run's. Without it a degradation recorded on an
	// earlier run survives every later one — succeed() will not overwrite it —
	// so installing the missing app, or switching the mode from the Setup tab,
	// leaves publishing and recording refused with the old reason still on
	// /status.
	ncAccessSubstrate.beginRun()

	probe, err := c.probeNCStorage(ctx, client, logger)
	if err != nil {
		logger.Printf("nc storage: %v — set %s to an account in the \"admin\" group; storage preflight skipped", err, envNCAdminUser)
		if errors.Is(err, errAdminRouteMissing) {
			ncAccessSubstrate.degraded("administrator_probe", err)
		} else {
			ncAccessSubstrate.unavailable("administrator", fmt.Errorf("%w; set %s to an account in the \"admin\" group", err, envNCAdminUser))
		}
		return
	}
	ncAccessSubstrate.setAdminUser(probe.AdminUser)
	ncAccessSubstrate.setPrerequisites(probe.Prereqs)
	ncAccessSubstrate.setProbe(probe)
	logger.Printf("nc storage: probe %s", summarizeProbe(probe))

	resolution := c.resolveStorageMode(logger)

	// Nothing recorded and nothing declared: resolve the mode from the archive
	// this instance already has, record it, and go on exactly as if an
	// administrator had chosen it — the adopt path, which moves nothing (D-753).
	//
	// What used to be here was terminal: no tree arranged, no legacy archive
	// adopted, nothing written down, and every recording on the instance refused
	// until somebody answered the setup wizard (D-708). The wizard's safety
	// argument is kept and moved into storageModeFromProbe, which reads the same
	// facts the wizard showed: Cassini never widens an existing archive on its
	// own. What is dropped is charging every call made before an administrator
	// noticed for a question the instance can answer.
	if !resolution.Decided {
		accessControlled, ok, why := storageModeFromProbe(probe)
		if !ok {
			// Not a decision anybody has to make, and not a missing
			// prerequisite either — the probe simply could not answer. Nothing
			// is written down on half the evidence.
			detail := storageModeUnresolvedDetail(why)
			ncAccessSubstrate.setMode("", "")
			logger.Printf("nc storage: %s", detail)
			ncAccessSubstrate.degraded(storageStepModeUnresolved, errors.New(detail))
			return
		}
		resolution = storageResolution{
			Decided:          true,
			AccessControlled: accessControlled,
			Source:           storageModeSourceResolved,
			Clean:            true,
		}
		// Written down BEFORE the sanity gate, unlike a declared mode. A
		// declaration is checked against the instance because the stack is
		// claiming something; this was read OFF the instance, so there is
		// nothing left to disagree with — and an install whose prerequisites are
		// broken is exactly the one whose archive must not be re-resolved from a
		// different instant once somebody starts repairing it.
		c.persistInitialMode(ncStorage.settingsPath(), accessControlled, storageModeSourceResolved,
			fmt.Sprintf("resolved the storage mode %q on enable because %s", storageModeName(accessControlled), why), logger)
	}

	accessControlled, source := resolution.AccessControlled, resolution.Source
	ncStorage.set(accessControlled, source, resolution.Clean)
	ncAccessSubstrate.setMode(storageModeName(accessControlled), source)

	if ok, step, detail := probe.sanity(accessControlled); !ok {
		logger.Printf("nc storage: mode=%s is not usable (%s): %s", storageModeName(accessControlled), step, detail)
		ncAccessSubstrate.unavailable(step, errors.New(detail))
		return
	}

	// A mode that is recorded but was never chosen used to stop here: a settings
	// file written by a build that fell back on its own, a first decision
	// interrupted between the dirty mark and the flip, a file this operator
	// could not parse. In all three the mode GOVERNS — the archive really is at
	// that root, and reads work — and the gate refused to arrange, adopt or
	// publish under it until an administrator confirmed the mode they already
	// had (D-708).
	//
	// It is gone (D-753). Confirming a fact is not a decision, and the cost of
	// asking was every recording on the instance. Where the mode came from is
	// still carried through to /storage in `mode_source`; it just gates nothing.

	// A DECLARED mode is checked against the instance before it is believed, and
	// written down only once it survives.
	//
	// The first pass persisted it immediately, so an administrator could read
	// back what they had declared even while the storage was still missing. That
	// was the right trade for a production affordance; this is a development and
	// CI one, the same value re-arrives on the next enable, and what matters more
	// is that a harness which declares a mode its own stack does not match finds
	// out loudly rather than recording the disagreement forever.
	if resolution.PersistAfterGates {
		if conflicts := probe.declaredModeConflicts(accessControlled); len(conflicts) > 0 {
			detail := declaredModeConflictDetail(accessControlled, conflicts)
			logger.Printf("ERROR: nc storage: %s", detail)
			ncAccessSubstrate.unavailable(storageStepDeclaredConflict, errors.New(detail))
			return
		}
		c.persistInitialMode(ncStorage.settingsPath(), accessControlled, source,
			fmt.Sprintf("%s declared the storage mode %q and this instance matches it", envStorageMode, storageModeName(accessControlled)), logger)
	}

	if err := c.arrangeRecordingsTree(ctx, client, accessControlled, logger); err != nil {
		return
	}

	// An install from before the roots were split keeps its default-mode archive
	// where the Team folder also wants to live. Carry it across, once, into the
	// root the default model now reads — otherwise the split would make an
	// administrator's recordings disappear with nothing on screen to explain it.
	// Non-fatal and self-terminating: the source is the state, so a run that does
	// not finish is finished by the next one.
	if !accessControlled {
		c.adoptLegacyDefaultArchive(ctx, client, probe, logger)
	}

	ncAccessSubstrate.succeed()
	logger.Printf("nc storage: ready mode=%s source=%s root=%s owner=%s", storageModeName(accessControlled), source, recordingsRootFor(accessControlled), ncRecordingsOwner)
}

// storageResolution is what one attempt to answer "which model is this install
// in" produced. Undecided is a value here, not the absence of one.
type storageResolution struct {
	// Decided is false when nobody has chosen. Everything else is meaningless
	// then, and the caller stops.
	Decided          bool
	AccessControlled bool
	// Source is the provenance, carried through rather than flattened. It is
	// what tells an administrator's click apart from a deploy option, and both
	// apart from a mode a previous build recorded on its own.
	Source string
	Clean  bool
	// PersistAfterGates marks a DECLARED mode that has not been written down
	// yet, because it has still to be checked against the instance.
	PersistAfterGates bool
}

// resolveStorageMode answers which model this process operates under, or says
// that nobody has decided.
//
//	recorded on disk               use it verbatim. Nothing re-opens it, ever.
//	CASSINI_STORAGE_MODE declared  that mode, checked against the instance and
//	                               written down only if it survives (dev/CI)
//	otherwise                      undecided HERE. The caller resolves it from
//	                               the probe (storageModeFromProbe, D-753).
//
// It never looks at the probe itself, and that separation is deliberate: a
// RECORDED decision must never be re-opened against what Nextcloud happens to
// look like on whichever enabled edge fired first, which on a stack still being
// assembled is the wrong instant. Reading the instance is only ever allowed to
// answer the question nobody has answered yet, once, and the answer is then
// recorded like any other.
func (c ExAppConfig) resolveStorageMode(logger *log.Logger) storageResolution {
	path := ncStorage.settingsPath()
	if path != "" {
		settings, err := LoadStorageSettings(path)
		switch {
		case err != nil:
			// An unreadable file must not fall through to undecided either: an
			// access-controlled install whose file lost a byte would stop
			// refusing to serve the archive as its owner only because nothing
			// could say it was access-controlled. Keep the safe model, say why,
			// and write nothing over a file we could not read.
			//
			// Reported as UNCONFIRMED, so the Setup tab offers a decision rather
			// than presenting one — writing a mode is how an administrator gets
			// out of this state.
			logger.Printf("ERROR: nc storage: %v — keeping access control ON until the file is readable or removed", err)
			// Clean, deliberately. An unreadable file is not evidence that a
			// migration is half done, and reporting one would offer a cleanup
			// that DELETES from a root chosen on the strength of a mode this
			// branch only assumed.
			return storageResolution{Decided: true, AccessControlled: true, Source: storageModeSourceConfigured, Clean: true}
		case settings.Configured():
			source := settings.Source
			if source == "" {
				// A file from a build that predates the field. Unknown
				// provenance reads as unconfirmed, which costs one question and
				// buys the guarantee that nothing presents an unmade decision as
				// a made one.
				source = storageModeSourceConfigured
			}
			return storageResolution{
				Decided:          true,
				AccessControlled: settings.AccessControlled(),
				Source:           source,
				Clean:            settings.Clean(),
			}
		}
	}

	// Nothing recorded. A declaration is a decision — but a development and CI
	// one, so it is believed only where it fits, and written down by the caller
	// once the gates agree.
	//
	// An unrecognised value is not silently ignored: starting an instance in a
	// mode nobody asked for is the failure this variable exists to prevent.
	declared, ok, raw := storageModeFromEnv(os.Getenv)
	switch {
	case ok:
		logger.Printf("WARNING: nc storage: %s=%s declared the storage mode %q. That deploy option is for development and CI; a production install is asked in the Setup tab", envStorageMode, raw, storageModeName(declared))
		return storageResolution{
			Decided:           true,
			AccessControlled:  declared,
			Source:            storageModeSourceEnv,
			Clean:             true,
			PersistAfterGates: true,
		}
	case raw != "":
		logger.Printf("ERROR: nc storage: %s=%q is not %s; ignoring it. No storage mode has been chosen, so Cassini will not publish until one is", envStorageMode, raw, storageModeEnvValues)
	}

	return storageResolution{}
}

// persistInitialMode writes the first decision an install makes and says where
// it came from. A failed write is not fatal — the mode still governs this
// process — but it is logged loudly, because the decision would then be made
// again on the next enable.
func (c ExAppConfig) persistInitialMode(path string, accessControlled bool, source, why string, logger *log.Logger) {
	if path == "" {
		logger.Printf("nc storage: %s; no settings path configured, so it governs this process only", why)
		return
	}
	// A first decision is settled by construction: nothing has ever migrated on
	// this install, so there is no source to have failed to clear.
	if err := SaveStorageSettings(path, accessControlled, source, true); err != nil {
		logger.Printf("ERROR: nc storage: %s, but it could not be written to %s: %v — it will be decided again on the next enable", why, path, err)
		return
	}
	logger.Printf("nc storage: %s and wrote %s", why, path)
}

// arrangeRecordingsTree makes the archive's own directories and, under access
// control, its container and per-leaf rules — inside storage whose
// prerequisites the sanity check has already confirmed are there.
//
// The line it draws is between PREREQUISITES (accounts, groups, apps, the Team
// folder — an administrator's, never created on this path) and the app's OWN
// tree inside them. A MKCOL in the service account's home creates nothing
// anybody has to consent to.
//
// Under access control it delegates to provisionNCFilesAccess unchanged, and
// that is deliberate rather than lazy: every install that exists today runs
// that function on every enable, its steps read before they write, and it
// carries the D-534/D-594 ordering the archive's safety rests on. Reaching it
// only through the readiness gate above is what makes it stop being a
// provisioner — with every prerequisite already satisfied there is nothing left
// for it to create, so what remains is exactly the arrangement: the canonical
// collections, the container ACL, and the leaf self-heal.
func (c ExAppConfig) arrangeRecordingsTree(ctx context.Context, client *http.Client, accessControlled bool, logger *log.Logger) error {
	if accessControlled {
		c.provisionNCFilesAccessLocked(ctx, logger)
		return nil
	}
	if err := c.mkcolRecordingsTree(ctx, client, ncDefaultRecordingsRoot); err != nil {
		logger.Printf("nc storage: create %s as %q: %v", ncDefaultRecordingsRoot, ncRecordingsOwner, err)
		ncAccessSubstrate.degraded("recordings_tree", err)
		return err
	}
	return nil
}

// mkcolRecordingsTree materializes a recordings root's collections as the
// service account. MKCOL of an existing collection is a 405, which davMkcol
// treats as success, so this is safe on every run against every root.
func (c ExAppConfig) mkcolRecordingsTree(ctx context.Context, client *http.Client, root string) error {
	for _, dir := range recordingsTreeDirs(root) {
		if err := c.davMkcol(ctx, client, ncRecordingsOwner, dir); err != nil {
			return fmt.Errorf("mkcol %s: %w", dir, err)
		}
	}
	return nil
}
