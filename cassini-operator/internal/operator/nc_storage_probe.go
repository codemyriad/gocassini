package operator

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// One read-only look at what the Nextcloud side of this install actually is
// (D-616 first pass).
//
// Before the opt-in there was nothing to look at: there was one storage model,
// provisioning built it, and the only question was whether each build step
// worked. With two models the question comes first — WHICH model is this
// instance set up for? — and it has to be answered without changing anything,
// because the answer is what decides whether a change would be safe.
//
//	                       ┌───────────────────────────────┐
//	                       │  administrator (act-as probe) │
//	                       └───────────────┬───────────────┘
//	                                       │
//	   ┌───────────────────────────────────┼───────────────────────────────┐
//	   ▼                                   ▼                               ▼
//	native apps                     `cassini` account                group folder
//	groupfolders                    exists?                          mount `Cassini`
//	group_everyone                                                   acl on?
//	   │                                   │                         everyone READ?
//	   │                            `everyone` group                 cassini ALL?
//	   │                            exists?                          cassini manages?
//	   └───────────────────────────────────┼───────────────────────────────┘
//	                                       ▼
//	                          Cassini/Recordings as `cassini`
//	                          (a private home tree only when no
//	                           Team folder is mounted over it)
//
// Every call here is a GET or a PROPFIND. Nothing is created, nothing is
// mapped, nothing is PROPPATCHed. That is the first-pass contract: the
// prerequisites for either model are the administrator's to set up, and the app
// only says which ones are missing.

// ncStorageProbe is what one probe run learned. Every field is an observation,
// never a decision — the decisions are accessControlReady/defaultReady below,
// so a caller can report the same facts under either mode.
type ncStorageProbe struct {
	// AdminUser is the account the probe resolved and acted as.
	AdminUser string
	// Prereqs is the per-app native prerequisite report, in the same shape
	// /status has carried since D-585.
	Prereqs []ncPrerequisiteStatus
	// NativeApps is true when both groupfolders and group_everyone are enabled.
	NativeApps bool
	// ServiceAccount is true when the `cassini` account exists. It is the one
	// prerequisite BOTH models need: every WebDAV write acts as it, in a Team
	// folder and in a private home alike.
	ServiceAccount bool
	// OwnerGroup is true when the narrow `cassini` group exists. Tracked apart
	// from the account because the two can genuinely come apart — an
	// administrator who ran `occ user:add` without `--group` has one and not the
	// other — and the Team folder's write mapping is onto the GROUP, so a plan
	// that inferred the group from the account would emit a mapping step for a
	// group that does not exist.
	OwnerGroup bool
	// EveryoneGroup is true when the virtual all-users group is present. Only
	// checked when group_everyone is enabled — an ordinary group of that name
	// would be a trap, not a substitute (see nc_provision.go step 1).
	EveryoneGroup bool

	// FolderProbed says the Team-folder question was ANSWERED — either the
	// folder list was read, or `groupfolders` is definitely not enabled and so
	// nothing can be mounted. It is NOT the same as FolderPresent being false,
	// and conflating the two is how "we could not look" becomes "there is
	// nothing there": the default model's whole safety argument is that no Team
	// folder shadows the service account's home, and an unanswered question is
	// not evidence of that.
	FolderProbed bool
	// Folder and the booleans under it describe the `Cassini` Team folder.
	// FolderPresent says it exists; FolderMounted says at least one group maps
	// to it, which is what makes it appear in anybody's Files — and therefore
	// what makes it shadow a same-named home directory.
	Folder        gfFolder
	FolderPresent bool
	FolderMounted bool
	ACLEnabled    bool
	EveryoneRead  bool
	OwnerAll      bool
	OwnerManages  bool

	// RecordingsRoot is true when Cassini/Recordings is reachable as the
	// service account, whichever storage it resolves to.
	RecordingsRoot bool
	// PrivateRoot is true when that root is the service account's OWN
	// directory rather than a mounted Team folder. There is no property to read
	// for this: a mounted Team folder wins the canonical path and the home
	// directory of the same name is renamed out of the way by the server (D-660
	// bench), so "reachable, and nothing is mounted over it" IS the test.
	PrivateRoot bool
}

// prereqEnabled reports whether one native app was positively reported as
// enabled. An `unknown` state — the check itself failed — is not enabled.
func prereqEnabled(prereqs []ncPrerequisiteStatus, name string) bool {
	for _, p := range prereqs {
		if p.Name == name {
			return p.State == ncPrerequisiteEnabled
		}
	}
	return false
}

// prereqsAnswered reports whether Nextcloud actually told us which apps are
// enabled. It is the difference between "that app is off" and "we could not
// ask", which for the Team-folder question decides whether a `false` means
// anything at all.
func prereqsAnswered(prereqs []ncPrerequisiteStatus) bool {
	if len(prereqs) == 0 {
		return false
	}
	for _, p := range prereqs {
		if p.State == ncPrerequisiteUnknown {
			return false
		}
	}
	return true
}

// storageProbeStep is a machine-readable name for the thing that is missing,
// in the same vocabulary /status has always used for provisioning steps, so a
// monitor or a test can key on it.
const (
	storageStepServiceAccount = "owner_account"
	storageStepUniversalGroup = "universal_group"
	storageStepGroupFolder    = "group_folder"
	storageStepFolderMount    = "group_folder_mount"
	storageStepFolderACL      = "group_folder_acl"
	storageStepFolderManager  = "group_folder_manager"
	// storageStepFolderUnknown means the Team-folder question could not be
	// answered. Under the default model that is disqualifying rather than
	// merely unfortunate: the model's safety rests on nothing being mounted
	// over the canonical path, and "we could not look" is not evidence of that.
	storageStepFolderUnknown = "group_folder_unknown"
	// storageStepModeMismatch means the recorded mode and the storage disagree.
	// Nothing is missing; the two just are not the same thing, and writing
	// under that disagreement is how recordings end up somewhere nobody is
	// looking (or somewhere everybody can read).
	storageStepModeMismatch = "mode_mismatch"
)

// probeNCStorage answers "what is this instance set up for", acting as the
// resolved administrator for the OCS/Group-Folders reads and as the service
// account for the one WebDAV read.
//
// It returns an error ONLY when the administrator could not be resolved. A
// missing app, a missing account, an absent folder are all ANSWERS — the
// probe's whole job — and each lands in the struct rather than in err.
func (c ExAppConfig) probeNCStorage(ctx context.Context, client *http.Client, logger *log.Logger) (ncStorageProbe, error) {
	var probe ncStorageProbe

	admin, err := c.resolveAdminIdentity(ctx, client, logger)
	if err != nil {
		return probe, err
	}
	probe.AdminUser = admin

	prereqs, perr := c.preflightNativeApps(ctx, client)
	probe.Prereqs = prereqs
	probe.NativeApps = perr == nil

	// The service account first, and unconditionally. It is the prerequisite
	// the default model rests on entirely, and asking about it after the native
	// apps — which is the order provisioning used — meant a deps-free instance
	// returned before anything ever looked (D-616 triage, correction 2).
	if exists, err := c.userExists(ctx, client, ncRecordingsOwner); err != nil {
		logger.Printf("nc storage: check service account %q: %v", ncRecordingsOwner, err)
	} else {
		probe.ServiceAccount = exists
	}

	if exists, err := c.groupExists(ctx, client, ncRecordingsOwnerGroup); err != nil {
		logger.Printf("nc storage: check owner group %q: %v", ncRecordingsOwnerGroup, err)
	} else {
		probe.OwnerGroup = exists
	}

	if prereqEnabled(prereqs, ncAppEveryoneGroup) {
		if exists, err := c.groupExists(ctx, client, ncRecordingsEveryoneGroup); err != nil {
			logger.Printf("nc storage: check universal group %q: %v", ncRecordingsEveryoneGroup, err)
		} else {
			probe.EveryoneGroup = exists
		}
	}

	// The Team folder is read on its own condition, not on NativeApps.
	//
	// Bundling it with the Everyone Group app is what made this dangerous: on an
	// instance where `group_everyone` is off but `groupfolders` is on and a
	// mapped `Cassini` folder is still shadowing the canonical path, the folder
	// was never looked at, `FolderMounted` stayed false, and an
	// access-controlled archive read as an unmounted one — which the default
	// model then serves to everybody. The two apps answer different questions
	// and are asked separately.
	switch {
	case !prereqsAnswered(prereqs):
		// Nextcloud did not say which apps are enabled, so we cannot even
		// conclude that a Team folder is impossible. Unanswered, not absent.
		probe.FolderProbed = false
	case !prereqEnabled(prereqs, ncAppGroupFolders):
		// The app is not enabled, so no Team folder is mounted anywhere. That is
		// an answer, and it is the one that makes a deps-free instance usable.
		probe.FolderProbed = true
	default:
		folder, ok, err := c.findFolder(ctx, client, ncRecordingsMount)
		if err != nil {
			logger.Printf("nc storage: list Team folders: %v", err)
			break
		}
		probe.FolderProbed = true
		if ok {
			probe.Folder = folder
			probe.FolderPresent = true
			probe.ACLEnabled = folder.ACL
			everyonePerms, everyoneMapped := folder.groupPerms(ncRecordingsEveryoneGroup)
			ownerPerms, ownerMapped := folder.groupPerms(ncRecordingsOwnerGroup)
			probe.EveryoneRead = everyoneMapped && everyonePerms&aclPermRead != 0
			probe.OwnerAll = ownerMapped && ownerPerms == aclMaskAll
			probe.FolderMounted = folder.anyGroupMapped()
			probe.OwnerManages = folder.hasManager("user", ncRecordingsOwner)
		}
	}

	// The one WebDAV read. It answers as the service account, so an instance
	// where the account does not exist would only get a 401 — skip it and let
	// the missing account be the diagnosis.
	if probe.ServiceAccount {
		_, visible, err := c.davPropfindNames(ctx, client, ncRecordingsOwner, ncRecordingsRoot)
		if err != nil {
			logger.Printf("nc storage: inspect %s as %q: %v", ncRecordingsRoot, ncRecordingsOwner, err)
		} else {
			probe.RecordingsRoot = visible
			probe.PrivateRoot = visible && !probe.FolderMounted
		}
	}
	return probe, nil
}

// accessControlReady reports whether every prerequisite of the access-
// controlled model is satisfied, and when it is not, the step to act on plus a
// sentence saying what to do about it.
//
// The order is the order an administrator installs them in, so the first thing
// reported is the first thing to do.
func (p ncStorageProbe) accessControlReady() (ok bool, step, detail string) {
	if missing := firstMissingApp(p.Prereqs); missing != "" {
		return false, "app_missing:" + missing,
			fmt.Sprintf("the %q app is not enabled; an ExApp cannot install it — run `occ app:install %s && occ app:enable %s`", missing, missing, missing)
	}
	if !p.NativeApps {
		return false, "app_check_failed",
			"Nextcloud did not answer which apps are enabled, so the access-controlled prerequisites could not be checked"
	}
	if !p.ServiceAccount {
		return false, storageStepServiceAccount, missingServiceAccountDetail()
	}
	if !p.EveryoneGroup {
		return false, storageStepUniversalGroup,
			fmt.Sprintf("the universal group %q does not exist; the %s app is enabled but produced no group", ncRecordingsEveryoneGroup, ncAppEveryoneGroup)
	}
	if !p.FolderPresent {
		return false, storageStepGroupFolder,
			fmt.Sprintf("there is no %q Team folder — create it with `occ groupfolders:create %s`", ncRecordingsMount, ncRecordingsMount)
	}
	folderID, _ := p.Folder.idInt()
	if !p.OwnerAll {
		return false, "mount_mapping:" + ncRecordingsOwnerGroup,
			fmt.Sprintf("the %q group has no write mount of the %q Team folder — run `occ groupfolders:group %d %s read write share delete`", ncRecordingsOwnerGroup, ncRecordingsMount, folderID, ncRecordingsOwnerGroup)
	}
	if !p.EveryoneRead {
		return false, "mount_mapping:" + ncRecordingsEveryoneGroup,
			fmt.Sprintf("the %q group has no read mount of the %q Team folder, so nobody can traverse to the recordings — run `occ groupfolders:group %d %s read`", ncRecordingsEveryoneGroup, ncRecordingsMount, folderID, ncRecordingsEveryoneGroup)
	}
	if !p.ACLEnabled {
		return false, storageStepFolderACL,
			fmt.Sprintf("advanced ACL is off on the %q Team folder, so there is no default-deny floor and every account could read every recording — run `occ groupfolders:permissions %d --enable`", ncRecordingsMount, folderID)
	}
	if !p.OwnerManages {
		return false, storageStepFolderManager,
			fmt.Sprintf("%q is not an ACL manager of the %q Team folder, so it cannot write a recording's audience — run `occ groupfolders:permissions %d -m --user %s`", ncRecordingsOwner, ncRecordingsMount, folderID, ncRecordingsOwner)
	}
	return true, "", ""
}

// defaultReady reports whether the deps-free model can be used. It needs one
// thing: the account that owns the tree. The tree itself is created on demand.
func (p ncStorageProbe) defaultReady() (ok bool, step, detail string) {
	if !p.ServiceAccount {
		return false, storageStepServiceAccount, missingServiceAccountDetail()
	}
	return true, "", ""
}

func missingServiceAccountDetail() string {
	return fmt.Sprintf(
		"the %q service account does not exist; every recording is written and read as it, so nothing can be stored without it — create it with `occ group:add %s` and `occ user:add --group=%s %s`",
		ncRecordingsOwner, ncRecordingsOwnerGroup, ncRecordingsOwnerGroup, ncRecordingsOwner)
}

// The probe does not decide the mode. It used to — deriveAccessControlEnabled()
// answered "this instance has the whole access-controlled substrate, so it must
// want access control" — and that made who can read the archive a function of
// what Nextcloud looked like at one instant. A substrate built with `occ`
// moments earlier may not have reached the web workers this probe asks, so the
// answer was a race, and it was permanent once recorded.
//
// What replaced it is deliberately duller: the mode comes from the settings file
// or CASSINI_STORAGE_MODE or nothing, and the probe's only job is sanity() below
// — does the storage match the mode it was told? A disagreement is reported, not
// resolved.

// sanity compares a mode against the storage and reports the disagreement.
//
// The two failures it can name are different in kind:
//
//	access controlled, not ready   something the model needs is missing. The
//	                               step names it and the detail says what to run.
//	default, but mounted           nothing is missing — there is a `Cassini`
//	                               Team folder mapped to a group, so it wins the
//	                               canonical path and every write the default
//	                               model makes lands inside the shared folder
//	                               instead of the service account's own home
//	                               (measured, D-660). Publishing under that
//	                               belief puts recordings somewhere the read
//	                               path is not looking.
func (p ncStorageProbe) sanity(accessControlled bool) (ok bool, step, detail string) {
	if accessControlled {
		return p.accessControlReady()
	}
	if ready, step, detail := p.defaultReady(); !ready {
		return false, step, detail
	}
	if !p.FolderProbed {
		// Refusing here is what keeps an unanswered question from being read as
		// a clean bill of health. The default model serves the whole archive as
		// its owner, so "no Team folder is in the way" has to be something we
		// KNOW, not something we failed to disprove.
		return false, storageStepModeMismatch + ":" + storageStepFolderUnknown,
			fmt.Sprintf(
				"Cassini could not determine whether a %q Team folder is mounted over %q, so it will not assume there is none — a mounted folder would put recordings somewhere the default mode is not looking. Check that Nextcloud is answering and re-enable Cassini",
				ncRecordingsMount, ncRecordingsRoot)
	}
	if p.FolderMounted {
		return false, storageStepModeMismatch + ":" + storageStepFolderMount,
			fmt.Sprintf(
				"access control is off, but a %q Team folder is still mapped to a group. A mounted Team folder wins the %q path, so recordings would be written into the shared folder rather than %q's own home. This is also what an access-controlled installation looks like to a Cassini that has not been told which mode it is in: if that is this instance, turn access control on in the Setup tab, or set %s=%s and re-enable the app. Otherwise unmap the Team folder's groups (`occ groupfolders:group <id> <group> --delete`) once its recordings have been moved out",
				ncRecordingsMount, ncRecordingsMount, ncRecordingsOwner, envStorageMode, storageModeAccessControlled)
	}
	return true, "", ""
}

// summarizeProbe is the one line the operator log carries per preflight, so an
// administrator reading the container log sees the same facts /storage reports.
func summarizeProbe(p ncStorageProbe) string {
	fields := []string{
		fmt.Sprintf("admin=%s", p.AdminUser),
		fmt.Sprintf("service_account=%t", p.ServiceAccount),
		fmt.Sprintf("owner_group=%t", p.OwnerGroup),
		fmt.Sprintf("native_apps=%t", p.NativeApps),
		fmt.Sprintf("everyone_group=%t", p.EveryoneGroup),
		fmt.Sprintf("folder_probed=%t", p.FolderProbed),
		fmt.Sprintf("team_folder=%t", p.FolderPresent),
		fmt.Sprintf("mounted=%t", p.FolderMounted),
		fmt.Sprintf("acl=%t", p.ACLEnabled),
		fmt.Sprintf("private_root=%t", p.PrivateRoot),
	}
	return strings.Join(fields, " ")
}
