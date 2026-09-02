package operator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
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

	accessControlled, source := c.resolveStorageMode(probe, logger)
	ncStorage.set(accessControlled, source)
	ncAccessSubstrate.setMode(storageModeName(accessControlled), source)

	if ok, step, detail := probe.sanity(accessControlled); !ok {
		logger.Printf("nc storage: mode=%s is not usable (%s): %s", storageModeName(accessControlled), step, detail)
		ncAccessSubstrate.unavailable(step, errors.New(detail))
		return
	}

	if err := c.arrangeRecordingsTree(ctx, client, accessControlled, logger); err != nil {
		return
	}

	ncAccessSubstrate.succeed()
	logger.Printf("nc storage: ready mode=%s source=%s root=%s owner=%s", storageModeName(accessControlled), source, ncRecordingsRoot, ncRecordingsOwner)
}

// resolveStorageMode returns the mode this process operates under, and where it
// came from.
//
// A flag that is already on disk is used verbatim and never re-derived. A flag
// that is absent is derived from the probe AND persisted, so the derivation
// happens exactly once in an install's life: after that the recorded value is
// what an administrator can reason about, and a Nextcloud that changes
// underneath the app cannot quietly change who may read the archive.
//
// A failed write is not fatal — the derived mode still governs this process, so
// the app works — but it is logged loudly, because the derivation would then
// run again on the next enable.
func (c ExAppConfig) resolveStorageMode(probe ncStorageProbe, logger *log.Logger) (accessControlled bool, source string) {
	path := ncStorage.settingsPath()
	if path != "" {
		settings, err := LoadStorageSettings(path)
		switch {
		case err != nil:
			// An unreadable file must not fall through to a derivation: the
			// derived answer for an instance whose Team folder has since been
			// removed is `default`, which would publish the next recording where
			// every account can read it. Keep the safe model and say why.
			logger.Printf("ERROR: nc storage: %v — keeping access control ON until the file is readable or removed", err)
			return true, storageModeSourceConfigured
		case settings.Configured():
			// A DERIVED default gets reconsidered, in one direction only.
			//
			// The derivation happens once, on whichever enabled edge comes
			// first, and that is not always a moment the instance is finished:
			// a substrate built with `occ` moments earlier may not have reached
			// the web workers the probe asks, so a fully access-controlled
			// Nextcloud can derive `default` and be stuck with it — publishing
			// refused, `mode_mismatch` forever, and no way back. That is not
			// hypothetical; it is what the installed-ExApp e2e caught.
			//
			// Adopting access control here can only ever NARROW who can read the
			// archive, so it cannot cause the disclosure the latch exists to
			// prevent. A mode an administrator CHOSE is never touched.
			if !settings.Chosen() && !settings.AccessControlled() && probe.deriveAccessControlEnabled() {
				logger.Printf("nc storage: the recorded %q mode was derived, but this instance has a complete access-controlled substrate — adopting access control (a derived mode is only ever reconsidered towards the more restrictive one)", storageModeDefault)
				if err := SaveStorageSettings(path, true, storageModeSourceDerived); err != nil {
					logger.Printf("ERROR: nc storage: could not persist the corrected mode to %s: %v", path, err)
				}
				return true, storageModeSourceDerived
			}
			return settings.AccessControlled(), storageModeSourceConfigured
		}
	}
	// Nothing recorded yet. A deployment that DECLARED which model it wants is
	// believed before anything is guessed — deriving is a reading of whatever
	// Nextcloud looked like at this instant, and on a stack still being built
	// that instant is the wrong one.
	//
	// An unrecognised value is not silently ignored: starting an instance in a
	// mode nobody asked for is the failure this variable exists to prevent.
	declared, ok, raw := storageModeFromEnv(os.Getenv)
	switch {
	case ok:
		return c.persistInitialMode(path, declared, storageModeSourceEnv,
			fmt.Sprintf("%s=%s declared the initial storage mode %q", envStorageMode, raw, storageModeName(declared)), logger)
	case raw != "":
		logger.Printf("ERROR: nc storage: %s=%q is not %s; ignoring it and deriving the mode from this instance instead", envStorageMode, raw, storageModeEnvValues)
	}

	derived := probe.deriveAccessControlEnabled()
	return c.persistInitialMode(path, derived, storageModeSourceDerived,
		fmt.Sprintf("no storage mode was recorded; derived %q from this instance", storageModeName(derived)), logger)
}

// persistInitialMode writes the first decision an install makes and says where
// it came from. A failed write is not fatal — the mode still governs this
// process — but it is loud, because the decision would then be made again on
// the next enable, possibly differently.
func (c ExAppConfig) persistInitialMode(path string, accessControlled bool, source, why string, logger *log.Logger) (bool, string) {
	if path == "" {
		logger.Printf("nc storage: %s; no settings path configured, so it governs this process only", why)
		return accessControlled, source
	}
	if err := SaveStorageSettings(path, accessControlled, source); err != nil {
		logger.Printf("ERROR: nc storage: %s, but it could not be written to %s: %v — it will be decided again on the next enable", why, path, err)
	} else {
		logger.Printf("nc storage: %s and wrote %s", why, path)
	}
	return accessControlled, source
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
	if err := c.mkcolRecordingsTree(ctx, client); err != nil {
		logger.Printf("nc storage: create %s as %q: %v", ncRecordingsRoot, ncRecordingsOwner, err)
		ncAccessSubstrate.degraded("recordings_tree", err)
		return err
	}
	return nil
}

// mkcolRecordingsTree materializes the canonical collections as the service
// account. MKCOL of an existing collection is a 405, which davMkcol treats as
// success, so this is safe on every run in either mode.
func (c ExAppConfig) mkcolRecordingsTree(ctx context.Context, client *http.Client) error {
	for _, dir := range recordingsTreeDirs(ncRecordingsRoot) {
		if err := c.davMkcol(ctx, client, ncRecordingsOwner, dir); err != nil {
			return fmt.Errorf("mkcol %s: %w", dir, err)
		}
	}
	return nil
}

// recordingsTreeDirs lists a recordings root's collections, outermost first, so
// MKCOL can walk them in creatable order. Derived from the root rather than
// hard-coded, because the transition builds the same shape under a staging name
// that is deliberately NOT the canonical mount point.
func recordingsTreeDirs(root string) []string {
	parts := strings.Split(strings.Trim(root, "/"), "/")
	dirs := make([]string, 0, len(parts)+1)
	for i := range parts {
		if parts[i] == "" {
			continue
		}
		dirs = append(dirs, strings.Join(parts[:i+1], "/"))
	}
	return append(dirs, strings.Trim(root, "/")+"/meetings")
}
