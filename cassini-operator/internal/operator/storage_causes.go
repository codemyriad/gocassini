package operator

import (
	"fmt"
	"strings"
)

// Why recordings cannot be saved, said in words (D-759).
//
// Every failure already has a machine-readable step and a detail sentence. The
// detail is written for whoever is going to FIX it: it quotes Nextcloud, names
// the folder id, and carries the `occ` line to run. That is the right sentence
// for the details block of a notice and the wrong one to open with, which is
// how the app came to lead with an enum name and a log line.
//
// So each step also gets a cause: one plain sentence saying what is wrong,
// with no command, no path and no enum name in it. The table lives here, beside
// the code that records the steps, for the same reason the mode copy does — a
// wrong instruction is worse than a missing one, and the layer that knows which
// step was recorded is the layer that should say what it means. The app renders
// it and decides nothing.
//
// Two audiences, as everywhere else in this file's neighbourhood:
//
//	admin  the sentence on the ADMIN-only /status block. It may name the
//	       service account and the Team folder, because whoever is reading it
//	       can already read both.
//	user   the same cause on the USER-level /setup, or empty when the honest
//	       cause cannot be said without naming an account, a path or an app
//	       that only an administrator may see. /setup's contract is that it
//	       carries the verdict and none of the diagnosis, and a cause sentence
//	       does not get to be the exception to it.
type storageCause struct {
	admin string
	user  string
}

// storageCauses maps an exact recordings_access step to its cause.
//
// Add an entry here whenever a new step is recorded through
// ncAccessSubstrate.unavailable or .degraded. storage_causes_test.go walks every
// step this package can record and fails when one has no sentence, which is what
// keeps this table from silently falling behind the code.
//
// Deliberately absent: `storage_mode_undecided` and `storage_mode_unconfirmed`.
// The operator resolves a mode when it is enabled (D-753), so neither state can
// be reached any more, and writing copy for a state nobody can be in is copy
// nobody will ever read. A step with no entry falls through to the app's own
// "the check did not finish" sentence rather than to silence.
var storageCauses = map[string]storageCause{
	storageStepServiceAccount: {
		admin: fmt.Sprintf(
			"Nextcloud is not letting the %s account write to its files. This usually means the account was removed or its group changed.",
			ncRecordingsOwner),
		user: "The Nextcloud account that recordings are saved as is missing, or it can no longer write to Nextcloud's files.",
	},
	storageStepUniversalGroup: {
		admin: "The Nextcloud group that lets everyone reach the recordings folder does not exist, even though the app that supplies it is enabled.",
		user:  "A Nextcloud group that recordings depend on does not exist.",
	},
	storageStepGroupFolder: {
		admin: "The Team folder that holds recordings does not exist on this Nextcloud.",
		user:  "The shared folder that holds recordings does not exist.",
	},
	storageStepFolderACL: {
		admin: "Advanced permissions are switched off on the Team folder that holds recordings, so nothing would stop every account reading every recording.",
		user:  "Permissions on the folder that holds recordings are switched off, so a recording cannot be kept to the people in the call.",
	},
	storageStepFolderManager: {
		admin: fmt.Sprintf(
			"The %s account does not manage permissions on the Team folder that holds recordings, so it cannot say who may read each one.",
			ncRecordingsOwner),
		user: "The account that saves recordings cannot say who may read each one.",
	},
	storageStepDeclaredConflict: {
		admin: "A deploy option names a rule for who can see recordings that this Nextcloud does not match, so nothing was written down.",
		user:  "A deploy option names a rule for who can see recordings that this Nextcloud does not match.",
	},
	"administrator": {
		admin: "Cassini could not find a Nextcloud administrator account to act as, so the recordings folder and its permissions were never created.",
		user:  "No Nextcloud administrator account could be found to finish setting recordings up.",
	},
	"administrator_probe": {
		admin: "Nextcloud did not answer when Cassini asked which account it should act as.",
		user:  "Nextcloud did not answer when it was asked who administers it.",
	},
	"app_check_failed": {
		admin: "Nextcloud did not answer when Cassini asked which apps are enabled, so it cannot tell whether the ones it needs are there.",
		user:  "Nextcloud did not answer when it was asked which apps are enabled.",
	},
	"universal_group_probe": {
		admin: "Nextcloud did not answer when Cassini asked whether the group behind read access exists.",
		user:  "Nextcloud did not answer when it was asked about a group recordings depend on.",
	},
	"folder_id": {
		admin: "Nextcloud described the Team folder that holds recordings without an id, so Cassini cannot set permissions on it.",
		user:  "Nextcloud described the folder that holds recordings in a way that could not be used.",
	},
	"legacy_deny_floor": {
		admin: "Nextcloud refused the rule that stops every account reading every recording by default.",
		user:  "Nextcloud refused the rule that stops every account reading every recording by default.",
	},
	"acl_enable": {
		admin: "Nextcloud refused to switch advanced permissions on for the Team folder that holds recordings, so the default-deny floor is missing.",
		user:  "Nextcloud refused to switch on the permissions that keep a recording to the people in the call.",
	},
	"acl_manager": {
		admin: fmt.Sprintf(
			"Nextcloud refused to let the %s account manage permissions on the Team folder, so it cannot say who may read each recording.",
			ncRecordingsOwner),
		user: "Nextcloud refused to let the account that saves recordings say who may read them.",
	},
	"migration_floor": {
		admin: "Nextcloud refused the owner-only floor Cassini applies before it touches recordings that are already there, so it stopped rather than widen anything.",
		user:  "Nextcloud refused a permission change, so recordings that are already there were left as they are.",
	},
	"migration": {
		admin: "Cassini could not carry the older recordings' access rules over, so the folder is left readable by its owner alone.",
		user:  "The access rules on older recordings could not be carried over, so the folder is readable by its owner alone.",
	},
	"catalog_migration": {
		admin: "Cassini could not carry the recording index's access rule over, so the folder is left readable by its owner alone.",
		user:  "The access rule on the list of recordings could not be carried over, so the folder is readable by its owner alone.",
	},
	"root_acl": {
		admin: "Nextcloud refused the rule that lets people into the recordings folder, so nobody can reach a recording in it.",
		user:  "Nextcloud refused the rule that lets people into the recordings folder, so nobody can reach a recording in it.",
	},
	"recordings_tree": {
		admin: "Nextcloud refused to create the folders recordings are filed under.",
		user:  "Nextcloud refused to create the folders recordings are filed under.",
	},
	storageStepModeMismatch + ":" + storageStepDefaultRootShadowed: {
		admin: "A Team folder is mounted where the private recordings directory belongs, so a recording would land in a shared folder instead.",
		user:  "A shared folder is mounted where recordings are kept privately, so a recording would be saved somewhere other people can read.",
	},
	storageStepModeMismatch + ":" + storageStepDefaultRootUnknown: {
		admin: "Nextcloud did not say whether a Team folder is mounted where recordings are kept, and Cassini will not write there until it knows.",
		user:  "Nextcloud did not say whether the place recordings are kept is private, so nothing will be saved there until it does.",
	},
}

// storageCausePrefixes covers the steps that carry a name in them —
// `app_missing:groupfolders`, `mount_mapping:everyone`, `mkcol:<dir>`,
// `mode_mismatch:<reason>`. The name is exactly the part a cause sentence must
// not repeat, so the family gets one sentence and the exact table above gets the
// reasons worth telling apart.
//
// Ordered, and matched in order: the longest, most specific prefix first, so
// `mode_mismatch:` cannot shadow an exact entry above or be shadowed by a
// shorter family here.
var storageCausePrefixes = []struct {
	prefix string
	cause  storageCause
}{
	{
		prefix: "app_missing:",
		cause: storageCause{
			admin: "A Nextcloud app that Cassini needs to show each person only their own recordings is switched off, and an external app cannot install it.",
			user:  "A Nextcloud app that recordings depend on is switched off.",
		},
	},
	{
		prefix: "mount_mapping:",
		cause: storageCause{
			admin: "Nextcloud refused to give one of the recording groups its mount of the Team folder, so the people in it cannot reach the recordings.",
			user:  "Nextcloud refused to give a group access to the folder that holds recordings.",
		},
	},
	{
		prefix: "mkcol:",
		cause: storageCause{
			admin: "Nextcloud refused to create one of the folders recordings are filed under.",
			user:  "Nextcloud refused to create one of the folders recordings are filed under.",
		},
	},
	{
		prefix: storageStepModeMismatch + ":",
		cause: storageCause{
			admin: "The rule for who can see recordings and the way this Nextcloud is set up disagree, so Cassini will not write a recording into a place the reading side is not looking.",
			user:  "Where recordings are kept does not match how this Nextcloud is set up.",
		},
	},
}

// storageCauseFor is the sentence for the ADMIN-only report. Empty when this
// build has nothing to say about the step, which the app treats as "the check
// did not finish" rather than inventing a reason.
func storageCauseFor(step string) string {
	return lookupStorageCause(step).admin
}

// storageUserCauseFor is the same cause for the USER-level /setup, or empty
// when the cause cannot be told without naming something only an administrator
// may see.
func storageUserCauseFor(step string) string {
	return lookupStorageCause(step).user
}

func lookupStorageCause(step string) storageCause {
	if step == "" {
		return storageCause{}
	}
	if cause, ok := storageCauses[step]; ok {
		return cause
	}
	for _, entry := range storageCausePrefixes {
		if strings.HasPrefix(step, entry.prefix) {
			return entry.cause
		}
	}
	return storageCause{}
}
